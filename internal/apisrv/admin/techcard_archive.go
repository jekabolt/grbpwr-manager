package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф1.5 — THE EXPORT RPC. The four halves built separately meet here and become one file.
//
// Nothing in this file decides WHAT travels — card.json is built by techcard_archive_card.go, the
// sidecars by techcard_archive_sidecars.go, the layout by internal/techcardarchive/writer.go and
// the object by internal/bucket/archive.go. What it owns is the JOIN: the manifest's passport and
// id maps, the pipe that keeps a gigabyte off the heap, and the order in which a failure of any of
// them leaves nothing behind.
//
// TWO THINGS WORTH SAYING OUT LOUD BEFORE THE CODE:
//
//   - THE ARCHIVE IS NEVER BUFFERED. WriteArchive runs in a goroutine writing into an io.Pipe and
//     the bucket's PutObject reads the other end. A card can legally carry hundreds of megabytes of
//     video; holding that in memory to learn its length would turn one export into an outage.
//   - AN EXPORT THAT PARTLY WORKED IS AN EXPORT THAT FAILED. If the writer dies mid-stream the pipe
//     carries the error to PutObject, which aborts its multipart upload and publishes no object, so
//     there is no half-archive to presign. That is why the writer never closes its zip on an error
//     path (a central directory over a truncated body would OPEN and be short) and why this handler
//     never presigns a key it did not see PutObject return.
// ─────────────────────────────────────────────────────────────────────────────

// archivePresignTTL is how long the download link lives. Ten minutes: the operator who pressed the
// button is looking at the screen, and the object is otherwise reachable by nothing at all (private
// upload, no public ACL). A longer window would buy a link that outlives the person's attention and
// travels in a chat log; a repeated export is one click.
const archivePresignTTL = 10 * time.Minute

// ExportTechCardArchive packs one tech card into a ZIP, puts it in a private bucket object and
// returns a short-lived link plus the archive's passport.
//
// RBAC is wr(tech_cards) and that is not a typo next to GetTechCard: this call carries private
// patterns and material passports OUT of the panel in one file. It is a deliberate release of the
// card, not a reading of it, so it takes the author's right rather than the reader's. (The map is
// in internal/rbac; nothing here re-checks it — one place per right.)
//
// The manifest travels back with the link on purpose. A person is about to hand this file to a
// factory, and the last honest moment to see WHAT IS NOT IN IT — the picture whose object the
// bucket would not give up, the material deleted from the catalogue — is before they send it, not
// after the factory asks.
func (s *Server) ExportTechCardArchive(ctx context.Context, req *pb_admin.ExportTechCardArchiveRequest) (*pb_admin.ExportTechCardArchiveResponse, error) {
	techCardID := int(req.GetTechCardId())
	if techCardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech card id is required")
	}

	// The same read the release snapshot takes, and for the same reason: an archive is a statement
	// about a card at a moment, and a replica lagging behind a save would produce a file that
	// disagrees with the screen the operator exported it from.
	card, err := s.repo.TechCards().GetTechCardByIdConsistent(ctx, techCardID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "tech card archive: can't load the card",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't export the tech card")
	}
	if card == nil {
		return nil, status.Error(codes.NotFound, "tech card not found")
	}

	cardJSON, cardHoles, err := buildArchiveCardJSON(card)
	if err != nil {
		slog.Default().ErrorContext(ctx, "tech card archive: can't build card.json",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't export the tech card")
	}

	sidecars, err := s.collectArchiveSidecars(ctx, card)
	// The defer sits directly under the call and before the error check, because the collector
	// cleans up after its OWN failures and this defer is what covers every later return: without it
	// a spooled gigabyte of media outlives the request in /tmp.
	defer sidecars.Close()
	if err != nil {
		return nil, archiveExportError(ctx, techCardID, "collect the archive content", err)
	}

	in, err := s.buildArchiveInput(ctx, card, cardJSON, append(cardHoles, sidecars.Holes...), sidecars)
	if err != nil {
		return nil, archiveExportError(ctx, techCardID, "assemble the archive manifest", err)
	}

	key, manifest, err := s.uploadTechCardArchive(ctx, in)
	if err != nil {
		return nil, archiveExportError(ctx, techCardID, "write the archive", err)
	}

	link, expiresAt, err := s.bucket.PresignArchiveObject(ctx, key, archivePresignTTL)
	if err != nil {
		slog.Default().ErrorContext(ctx, "tech card archive: can't presign the archive",
			slog.Int("tech_card_id", techCardID), slog.String("key", key), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "the archive was written but cannot be handed over")
	}

	s.journalArchiveExport(ctx, card, manifest)

	return &pb_admin.ExportTechCardArchiveResponse{
		Url:       link,
		ExpiresAt: timestamppb.New(expiresAt),
		Manifest:  archiveManifestToPb(manifest),
	}, nil
}

// archiveExportError turns an export failure into the ONE thing a caller can act on.
//
// The ceilings of the format are ResourceExhausted and everything else is Internal, because those
// are two different sentences to a human: «this card does not fit the format — split it or raise
// the ceiling» versus «something broke — try again or call somebody». The ceiling family is
// recognised by errors.Is over three sentinels (the writer's, and the collectors' one for spooled
// bytes) rather than by string matching, so a new call site cannot fall out of the family by
// rewording its message.
func archiveExportError(ctx context.Context, techCardID int, what string, err error) error {
	if errors.Is(err, techcardarchive.ErrArchiveTooLarge) || errors.Is(err, errArchiveContentTooLarge) {
		slog.Default().WarnContext(ctx, "tech card archive: the card does not fit the format",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		return status.Errorf(codes.ResourceExhausted,
			"this tech card is too large for one archive: %v", err)
	}
	slog.Default().ErrorContext(ctx, "tech card archive: can't "+what,
		slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
	return status.Error(codes.Internal, "can't export the tech card")
}

// uploadTechCardArchive streams the archive straight into a private bucket object and returns its
// key together with the manifest that was actually written.
//
// The pipe is the whole point. WriteArchive writes; PutObject reads; nothing between them is a
// buffer bigger than one multipart part. The two error directions are both closed:
//
//   - the writer fails → pw.CloseWithError makes the bucket's read fail → PutObject aborts and
//     publishes nothing;
//   - the upload fails → pr.CloseWithError unblocks the writer, which would otherwise sit on a
//     Write forever and leak the goroutine and its spooled file handles.
//
// The manifest comes back over a channel rather than a shared variable because the upload returning
// does not mean the writer's last statement has run.
func (s *Server) uploadTechCardArchive(ctx context.Context, in techcardarchive.ArchiveInput) (string, techcardarchive.Manifest, error) {
	pr, pw := io.Pipe()
	type written struct {
		manifest techcardarchive.Manifest
		err      error
	}
	done := make(chan written, 1)
	go func() {
		m, err := techcardarchive.WriteArchive(pw, in)
		// CloseWithError(nil) is Close: the reader sees a clean EOF and PutObject finishes the
		// object. With an error it sees that error and refuses to publish anything.
		pw.CloseWithError(err)
		done <- written{manifest: m, err: err}
	}()

	key, upErr := s.bucket.UploadArchiveObject(ctx, pr, archiveObjectFileName(in.Source.StyleNumber, in.ExportedAt))
	// UNCONDITIONALLY, and before the wait. On a failed upload this is what unblocks a writer
	// sitting on a Write nobody will ever read. On a SUCCESSFUL one it is a no-op in practice — an
	// upload can only finish after reading EOF, which only happens after the writer closed — and it
	// costs nothing to be right anyway: a consumer that returned success without draining would
	// otherwise park this goroutine and this request forever.
	pr.CloseWithError(upErr)
	res := <-done

	// The writer's verdict is read FIRST. A failure there is the honest cause even when the upload
	// also failed (it failed BECAUSE of this), and — the case that matters — an upload can succeed
	// on a truncated body if the writer died after the last part boundary, in which case this is
	// the only side that knows the object is not an archive.
	if res.err != nil {
		if key != "" {
			// Belt and braces: minio does not publish an aborted multipart, so this branch is not
			// expected to fire. If it ever does, the object is a truncated archive nobody must be
			// able to presign, and leaving it would be leaving a trap for the cleanup job to
			// «find» later.
			s.deleteArchiveObjectBestEffort(ctx, key)
		}
		return "", techcardarchive.Manifest{}, res.err
	}
	if upErr != nil {
		return "", techcardarchive.Manifest{}, fmt.Errorf("upload the archive: %w", upErr)
	}
	return key, res.manifest, nil
}

// deleteArchiveObjectBestEffort removes an object that must not survive. Best effort and loud: the
// export has already failed, and a second failure here changes nothing the caller can do — but a
// stray private object with half a tech card in it is worth a line in the log.
func (s *Server) deleteArchiveObjectBestEffort(ctx context.Context, key string) {
	if err := s.bucket.RemoveObjectsByKeys(ctx, key); err != nil {
		slog.Default().ErrorContext(ctx, "tech card archive: can't remove a partial archive object",
			slog.String("key", key), slog.String("err", err.Error()))
	}
}

// archiveObjectFileName builds techcard-<style_number>-<yyyymmdd-hhmm>.zip (FORMAT.md §1).
//
// The bucket sanitises this into a key-safe basename of its own accord — it has to, because the
// style number is typed by a person — so this is the display half only. A card with no style number
// (an `idea` draft) still exports: it becomes «techcard-<id>-…», which names something rather than
// nothing.
func archiveObjectFileName(styleNumber string, at time.Time) string {
	name := strings.TrimSpace(styleNumber)
	if name == "" {
		name = "unnumbered"
	}
	return fmt.Sprintf("techcard-%s-%s.zip", name, at.UTC().Format(techcardarchive.ArchiveNameTimeLayout))
}

// buildArchiveInput assembles everything WriteArchive needs that is not already collected: the
// passport, the id maps and the two halves of the content joined.
func (s *Server) buildArchiveInput(ctx context.Context, card *entity.TechCard, cardJSON []byte,
	holes []techcardarchive.ExportHole, sc *archiveSidecars,
) (techcardarchive.ArchiveInput, error) {
	di, err := s.repo.Cache().GetDictionaryInfo(ctx)
	if err != nil {
		return techcardarchive.ArchiveInput{}, fmt.Errorf("load dictionary for the manifest: %w", err)
	}

	// SIZES: THE WHOLE SOURCE DICTIONARY, merged with every name the collectors saw.
	//
	// §2 defines the map as «the ids that appear in card.json», and taking that literally is the
	// trap. §5.7 remaps EVERY size id inside a marker blob through this same table, and a mixed lay
	// (смешанный настил) names sizes the card need never mention — a miss there is not a missing
	// label, it is a size_unknown hole that drops the WHOLE marker at the far end. A superset is
	// explicitly legal and costs a few dozen short strings; a subset is a silent loss.
	sizes := make(map[string]string, len(di.Sizes)+len(sc.SizeNames))
	for _, sz := range di.Sizes {
		sizes[strconv.Itoa(sz.Id)] = sz.Name
	}
	for id, name := range sc.SizeNames {
		// The collectors' names win: they are what the sidecars and the marker blobs were actually
		// written with, and a disagreement here would mean the dictionary moved mid-export.
		sizes[strconv.Itoa(id)] = name
	}

	// COLOURWAYS: reference only. A colourway is a product, an import creates no products (§5.3),
	// and this map exists so a human reading the archive can tell which colour code the source's
	// numbers meant. The sidecar payload carries no product ids by construction, so the card is the
	// only place to take them from.
	colorways := make(map[string]string, len(card.Colorways))
	for _, cw := range card.Colorways {
		if cw.ColorCode == "" {
			continue
		}
		colorways[strconv.Itoa(cw.Id)] = cw.ColorCode
	}

	return techcardarchive.ArchiveInput{
		ExportedAt: time.Now().UTC(),
		ExportedBy: authsrv.GetAdminUsername(ctx),
		Source: techcardarchive.Source{
			Host:        archiveSourceHost(s.patternURLsBaseURL),
			TechCardID:  int32(card.Id),
			StyleNumber: card.StyleNumber.String,
			LockVersion: int32(card.LockVersion),
			// The state AT EXPORT, and provenance only: the import forces draft regardless
			// (FORMAT.md §6.1). It travels so a person can see that what they were handed was a
			// released card rather than somebody's sketch.
			ApprovalStateAtExport: string(card.ApprovalState),
			AppVersion:            archiveAppVersion(),
		},
		IDMaps: techcardarchive.IDMaps{
			Sizes:        sizes,
			CategoryPath: archiveCategoryPath(di.Categories, card),
			Colorways:    colorways,
		},
		Holes:       holes,
		CardJSON:    cardJSON,
		SizeChart:   sc.SizeChart,
		Assembly:    sc.Assembly,
		Colorways:   sc.Colorways,
		Materials:   sc.Materials,
		Media:       sc.Media,
		Patterns:    sc.Patterns,
		Markers:     sc.Markers,
		MarkerFiles: archiveMarkerFiles(sc.MarkerFiles),
		Files:       archiveBinaryFiles(sc.Blobs),
	}, nil
}

// archiveMarkerFiles / archiveBinaryFiles adapt the collector's types to the format package's.
//
// Two nearly identical structs rather than one shared type, because the arrow only points one way:
// internal/techcardarchive is a leaf and must not learn what a spool or a bucket is, and package
// admin must not export its internals to satisfy a signature. The copy is six lines and the
// compiler checks it.
func archiveMarkerFiles(in []archiveJSONFile) []techcardarchive.JSONFile {
	out := make([]techcardarchive.JSONFile, 0, len(in))
	for _, f := range in {
		out = append(out, techcardarchive.JSONFile{Name: f.Name, Data: f.Data})
	}
	return out
}

func archiveBinaryFiles(in []archiveBlob) []techcardarchive.BinaryFile {
	out := make([]techcardarchive.BinaryFile, 0, len(in))
	for _, b := range in {
		blob := b // the opener closes over its own copy, not over the loop's
		out = append(out, techcardarchive.BinaryFile{
			Name:   blob.Name,
			SHA256: blob.SHA256,
			Size:   blob.Size,
			Open:   blob.Open,
		})
	}
	return out
}

// archiveCategoryPath names the card's category triple, top level first.
//
// By NAME because ids are the source instance's; as a PATH rather than three fields because the
// import resolves it as a path (a "jacket" under "outerwear" is not the same node as a "jacket"
// somewhere else). An unset level ends the path: a gap in the middle would let the far end pair a
// top with a type that never sat under it.
func archiveCategoryPath(categories []entity.Category, card *entity.TechCard) []string {
	byID := make(map[int]string, len(categories))
	for _, c := range categories {
		byID[c.ID] = c.Name
	}
	out := make([]string, 0, 3)
	for _, id := range []sql.NullInt32{card.TopCategoryId, card.SubCategoryId, card.TypeId} {
		if !id.Valid {
			break
		}
		name, ok := byID[int(id.Int32)]
		if !ok || name == "" {
			break
		}
		out = append(out, name)
	}
	return out
}

// archiveSourceHost is provenance: WHERE this archive came from, in words a person recognises.
//
// Taken from the backend's own configured external origin (the same string the pattern viewer mints
// absolute urls against) and reduced to a host, because a full url in a field named `host` invites
// somebody to fetch it — and §2 is explicit that nothing may be resolved through `source`. Empty
// when the origin is not configured, which is honest: a blank field says «unknown», a guessed one
// would say something false.
func archiveSourceHost(baseURL string) string {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

// archiveAppVersion is the build's vcs revision, short.
//
// Read from the binary's own build info rather than from a constant somebody has to remember to
// bump: a constant that lies is worse than an empty field, and this is a support breadcrumb — «the
// archive that came out wrong was written by THAT build». Empty on a build without VCS stamping
// (`go test`, a dirty tree), which is exactly when the answer is unknown.
func archiveAppVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			rev := setting.Value
			if len(rev) > 7 {
				rev = rev[:7]
			}
			return rev
		}
	}
	return ""
}

// journalArchiveExport records the export in the card's auto-journal.
//
// BEST EFFORT, like the release snapshot and for the same reason: the archive exists and the link
// is minted, so failing the RPC now would tell the operator the export failed while a copy of their
// tech card sits in a bucket. A missing journal line is a gap in an audit trail; a false failure is
// a second export.
//
// It matters that the line exists at all: this is the one RPC that takes the whole card out of the
// building, and «who took it and when» is not derivable from anything else afterwards — the object
// expires, the link expires, and the card looks untouched.
//
// WithoutCancel for exactly that reason: by the time this runs the archive is in the bucket and the
// link is already minted, so a client that hangs up in between would take the only record of it
// having left with them. Cancellation carries no information here — it cannot un-export the file —
// and honouring it would turn a network hiccup into a hole in the audit trail. The context's VALUES
// ride along (so the write still carries whatever the request put there); its cancellation and its
// deadline do not — WithoutCancel drops both — and one INSERT is not something a deadline was
// protecting anything from.
func (s *Server) journalArchiveExport(ctx context.Context, card *entity.TechCard, m techcardarchive.Manifest) {
	ctx = context.WithoutCancel(ctx)
	summary := fmt.Sprintf("tech card exported as an archive (%d media, %d patterns, %d markers, %d materials, %d holes)",
		m.Contents.Media, m.Contents.Patterns, m.Contents.Markers, m.Contents.Materials, len(m.ExportHoles))
	if err := s.repo.TechCards().AppendTechCardArchiveExportedEvent(ctx, card.Id, m.ExportedBy, summary); err != nil {
		slog.Default().ErrorContext(ctx, "tech card archive: can't journal the export",
			slog.Int("tech_card_id", card.Id), slog.String("err", err.Error()))
	}
}

// archiveManifestToPb projects the manifest onto the wire.
//
// A PROJECTION AND NOT THE MANIFEST: id_maps do not travel here on purpose. They are machine
// dictionaries an importer reads out of the archive itself, they are the largest part of the file,
// and putting them on a panel response would be shipping a second copy that can disagree with the
// first.
//
// Counters are a list of pairs and reasons are strings, both by the contract's own decision (see
// the proto): a new entity or a new reason must reach an old client as an unfamiliar STRING rather
// than being dropped silently, which is what an enum member protojson does not know is worth.
func archiveManifestToPb(m techcardarchive.Manifest) *pb_admin.TechCardArchiveManifest {
	out := &pb_admin.TechCardArchiveManifest{
		Format:        m.Format,
		FormatVersion: m.FormatVersion,
		MoneyPolicy:   m.MoneyPolicy,
		ExportedAt:    timestamppb.New(m.ExportedAt),
		ExportedBy:    m.ExportedBy,
		Source: &pb_admin.TechCardArchiveSource{
			Host:                  m.Source.Host,
			TechCardId:            m.Source.TechCardID,
			StyleNumber:           m.Source.StyleNumber,
			LockVersion:           m.Source.LockVersion,
			ApprovalStateAtExport: m.Source.ApprovalStateAtExport,
			AppVersion:            m.Source.AppVersion,
		},
	}
	// The words are the JSON keys of manifest.contents, not the singular Entity* vocabulary of the
	// report: this list says «how many media files are in the archive», and the report's `media`
	// says «this one media had a problem». Printing the counter under the report's word would
	// invite a panel to join the two lists on it.
	for _, c := range []struct {
		entity string
		count  int
	}{
		{"media", m.Contents.Media},
		{"patterns", m.Contents.Patterns},
		{"markers", m.Contents.Markers},
		{"materials", m.Contents.Materials},
	} {
		out.Counters = append(out.Counters, &pb_admin.TechCardArchiveCounter{
			Entity: c.entity, Count: int32(c.count),
		})
	}
	for _, h := range m.ExportHoles {
		out.Holes = append(out.Holes, &pb_admin.TechCardArchiveHole{
			Entity: h.Entity,
			Ref:    h.Ref,
			Reason: string(h.Reason),
			Detail: h.Detail,
		})
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Ф3.3 — THE COMMIT, where an import stops being a preview.
//
// This is the only place the import path actually writes. Everything before it produced PLANS: the
// reader opened a ZIP, the resolver matched it against this base, the dry run answered with a
// report and nothing else. Here the files move (Ф3.1), the transaction runs (Ф3.2), and a card
// exists that did not exist before.
//
// FOUR DECISIONS LIVE IN THIS SECTION, AND EACH ONE IS THE ANSWER TO A WAY OF LOSING SOMETHING:
//
//   - THE DRY RUN IS NOT CACHED. The archive is re-opened and re-resolved, because the base moves:
//     a material was renamed, a size was retired, a style number was taken between the upload and
//     the click. The report the operator keeps forever describes what happened, not what would
//     have happened twenty minutes ago.
//
//   - A FAILED COMMIT DOES NOT PROVE A ROLLBACK. `TxCommit` can fail with the server having applied
//     the commit and the answer lost on the way back. The obvious reaction — «call compensate» —
//     then deletes the pattern objects of a card that is ALIVE, because pattern objects have no
//     foreign key protecting them the way media rows do. So compensation is gated on a POSITIVE
//     re-read of tech_card_import on a fresh connection: rolled back → take the files back;
//     committed → answer with the card that landed; could not tell → leave the objects to the
//     sweeper (Ф5.1). An orphaned object costs storage. A deleted object of a live card costs the
//     card.
//
//   - A PICTURE THAT VANISHED IS A HOLE, NEVER A REFUSAL. Ф3.1 mints media rows BEFORE this
//     transaction opens, so a parallel import can match one of them by content hash and plan to
//     reuse it; if the first import then compensates, the second one's foreign key has nothing to
//     point at. Losing one picture with a line in the report is the owner's binding rule; losing
//     the whole import is not. Hence the retry below.
//
//   - THE REPORT IS STAMPED BY THE STORE, INSIDE THE TRANSACTION. Only the transaction knows what
//     the WRITE dropped (chart cells outside the card's own size range, a grade base, areas, an
//     assembly line), so it amends the report and stores it in the same breath. This handler
//     therefore never stamps a copy of its own — it reads back what was stored and answers with
//     THAT, so the screen and the row cannot disagree.
// ─────────────────────────────────────────────────────────────────────────────

const (
	// tcciSettleTimeout bounds the ONE read that decides whether a failed commit may be compensated.
	// It runs on a DETACHED context on purpose: the commonest reason to be settling at all is a
	// context that has just died, and a settlement that cannot read the row answers «I do not know»
	// — which permanently forbids compensation. Honouring the dead context would make «I do not
	// know» the answer every single time.
	tcciSettleTimeout = 10 * time.Second

	// tcciErrorDomain / tcciReason* label the ErrorInfo a client branches on. Prose is for people;
	// a panel deciding whether to open the card that already exists must not match English.
	tcciErrorDomain            = "techcard-import.grbpwr"
	tcciReasonAlreadyCommitted = "TECH_CARD_IMPORT_ALREADY_COMMITTED"
	tcciReasonNotCommittable   = "TECH_CARD_IMPORT_NOT_COMMITTABLE"
)

// CommitTechCardImport turns an uploaded archive into a tech card.
//
// ONE ARCHIVE MAKES AT MOST ONE CARD. The guard is the tech_card_import row, checked here for a
// readable answer and claimed again inside the transaction for a correct one: two operators
// pressing the same button, a double click and a retried request are all the same event, and the
// loser of that race is told which card the winner made rather than being handed a second one.
//
// RBAC is wr(tech_cards) and lives in internal/rbac like every other method's — the interceptor has
// already run by the time this body starts.
func (s *Server) CommitTechCardImport(ctx context.Context, req *pb_admin.CommitTechCardImportRequest) (
	*pb_admin.CommitTechCardImportResponse, error) {

	importID := strings.TrimSpace(req.GetImportId())
	if importID == "" {
		return nil, status.Error(codes.InvalidArgument, "import_id is required")
	}

	row, err := s.repo.TechCards().GetTechCardImportByImportID(ctx, importID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound,
				"no uploaded archive %s — upload the file again", importID)
		}
		slog.Default().ErrorContext(ctx, "tech card import: can't read the import row",
			slog.String("import_id", importID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't commit the import")
	}
	if err := tcciCommittable(row); err != nil {
		return nil, err
	}

	// FROM HERE THE ARCHIVE IS READ AFRESH. See the section header: a cached dry run would describe
	// a base that has since moved, and the report this produces is the permanent one.
	ra, size, err := s.bucket.GetImportObjectReaderAt(ctx, row.ObjectKey)
	if err != nil {
		slog.Default().ErrorContext(ctx, "tech card import: can't read back the archive object",
			slog.String("import_id", importID), slog.String("object_key", row.ObjectKey),
			slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal,
			"the uploaded archive could not be read back; upload it again")
	}
	defer ra.Close()

	arch, err := techcardarchive.OpenArchive(ra, size)
	if err != nil {
		return nil, tcciArchiveError(ctx, importID, "read", err)
	}
	res, err := s.resolveTechCardImport(ctx, arch)
	if err != nil {
		return nil, tcciArchiveError(ctx, importID, "resolve", err)
	}

	// THE BYTES MOVE BEFORE THE TRANSACTION, because neither the bucket nor the media table is
	// transactional and a card cannot be written pointing at objects that do not exist. Everything
	// after this line owes the placement a compensate() unless a card was actually created — and
	// «actually created» is a question tcciSettle answers, never an assumption.
	place, err := s.tcflPlaceImportFiles(ctx, arch, res)
	if err != nil {
		// tcflPlaceImportFiles cleans up after its OWN failures and returns nil, so there is
		// nothing here to take back.
		return nil, tcciArchiveError(ctx, importID, "move the archive's files into", err)
	}

	card, markers, err := s.tcciPayload(ctx, res)
	if err != nil {
		place.compensate(ctx, s)
		return nil, err
	}

	return s.tcciWrite(ctx, tcciCommit{
		importID: importID,
		arch:     arch,
		res:      res,
		place:    place,
		card:     card,
		markers:  markers,
		actor:    card.CreatedBy,
	})
}

// tcciCommittable answers whether this upload row may still become a card, and says WHY not in
// words the operator can act on.
//
// The store claims the row again inside its own transaction — that claim is the correctness half,
// and it is what makes two simultaneous commits safe. This check is the KINDNESS half: it turns the
// commonest failure (a second click) into a sentence naming the card that already exists, instead
// of a race lost at the end of a full re-resolve.
func tcciCommittable(row entity.TechCardArchiveImportRecord) error {
	switch row.Status {
	case entity.TechCardImportStatusUploaded:
		return nil
	case entity.TechCardImportStatusCommitted:
		return tcciAlreadyCommitted(row)
	case entity.TechCardImportStatusExpired:
		return tcciNotCommittable(row,
			"this upload has expired — its archive was swept out of the bucket. Upload the file again.")
	case entity.TechCardImportStatusFailed:
		return tcciNotCommittable(row,
			"an earlier commit of this upload failed and it cannot be retried. Upload the file again.")
	default:
		return tcciNotCommittable(row,
			fmt.Sprintf("this upload is %q and cannot be committed", row.Status))
	}
}

// tcciAlreadyCommitted is the answer to the SECOND press of the button: FailedPrecondition carrying
// the id of the card the FIRST press made.
//
// The id travels in the status detail rather than only in the sentence because that is what the
// panel needs: «you already imported this — here is the card» is a navigation, and a client should
// not have to parse a number out of English to perform it. If the detail cannot be attached the
// refusal still goes out plain — losing the refusal would be worse than losing the breadcrumb.
func tcciAlreadyCommitted(row entity.TechCardArchiveImportRecord) error {
	msg := "this archive has already been imported"
	md := map[string]string{"import_id": row.ImportID}
	if row.TechCardID.Valid && row.TechCardID.Int32 > 0 {
		msg = fmt.Sprintf("%s — it created tech card %d", msg, row.TechCardID.Int32)
		md["tech_card_id"] = strconv.Itoa(int(row.TechCardID.Int32))
	} else {
		// tech_card_id is ON DELETE SET NULL and the row outlives its card deliberately, so this is
		// «the card it made has since been deleted», not «the import is unfinished». Importing the
		// same archive again is a fresh upload, which is what the sentence has to say.
		msg += ", and the card it created has since been deleted — upload the archive again to import it anew"
	}
	st := status.New(codes.FailedPrecondition, msg)
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason: tcciReasonAlreadyCommitted, Domain: tcciErrorDomain, Metadata: md,
	})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

// tcciNotCommittable is the same shape for a row that is neither fresh nor finished.
func tcciNotCommittable(row entity.TechCardArchiveImportRecord, msg string) error {
	st := status.New(codes.FailedPrecondition, msg)
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason: tcciReasonNotCommittable, Domain: tcciErrorDomain,
		Metadata: map[string]string{"import_id": row.ImportID, "status": row.Status},
	})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

// tcciArchiveError classifies what the reader, the resolver and the file mover refuse.
//
// Same vocabulary and the same fail-closed direction as the upload route's: corruption, a foreign
// MAJOR and a missing money policy are the caller's file (InvalidArgument); an error class nobody
// here has heard of is treated as OURS, because blaming a person's archive for a fault of our own
// is the more expensive mistake — they would go looking for a broken download that does not exist.
func tcciArchiveError(ctx context.Context, importID, phase string, err error) error {
	if errors.Is(err, techcardarchive.ErrRefused) ||
		errors.Is(err, techcardarchive.ErrCorrupt) ||
		errors.Is(err, techcardarchive.ErrNotFound) {
		slog.Default().InfoContext(ctx, "tech card import: the archive was refused on commit",
			slog.String("import_id", importID), slog.String("phase", phase),
			slog.String("err", err.Error()))
		return status.Error(codes.InvalidArgument, err.Error())
	}
	slog.Default().ErrorContext(ctx, "tech card import: can't "+phase+" the archive",
		slog.String("import_id", importID), slog.String("err", err.Error()))
	return status.Error(codes.Internal, "can't commit the import")
}

// ────────────────────────────── the payload, through the same gates a create passes ──────────────

// tcciPayload turns the resolver's plan into the entity payload the store takes, through EXACTLY
// the gates CreateTechCard applies (internal/apisrv/admin/techcard.go).
//
// It matters that they are the same ones and that they run at all. The insert reaching here was
// assembled by this server out of a file, so «the client would never send that» is not an argument
// available to it: a hand-made archive is a client with no bundle version and no manners. A gate
// failing means the archive contradicts the contract the card tables are written under — a corrupt
// archive, refused whole — and NOT a hole: a hole is a row that could not be placed, while this is
// a payload that may not be written at all.
//
// The stored gates get nil for the stored card, which is not a stub: an import CREATES, so there is
// no stored card and nothing to erase. That is the same nil CreateTechCard passes, for the same
// reason.
//
// IT RUNS AFTER THE FILES HAVE MOVED, and deliberately so. The conversion is the last of the three
// and it cannot run earlier: ConvertPbTechCardInsertToEntity refuses a negative media id and a
// pattern row with no url, which is exactly the state the payload is in until Ф3.1 has substituted
// the real ones — that refusal is the safety net against a write path that forgets the
// substitution. Keeping the gates beside it costs a refused archive the round trip of its own
// files, which compensation takes back; splitting them earlier would buy that back at the price of
// two places where a payload is judged.
//
// WHAT IS DELIBERATELY NOT COPIED FROM CreateTechCard: the sign-off preparation and the digest
// restamping. An imported card arrives a draft with no sign-offs, and the create pipeline COERCES
// supplied sign-offs into fresh ones stamped with the importing operator's name — so handing it any
// would be how you MINT a signature out of a file. The store strips them a third time; this is the
// second, and the first is the sanitiser.
func (s *Server) tcciPayload(ctx context.Context, res *resolvedTechCardImport) (
	*entity.TechCardInsert, []entity.TechCardMarkerInsert, error) {

	if err := tcciWireGates(res.Insert); err != nil {
		return nil, nil, err
	}
	card, err := dto.ConvertPbTechCardInsertToEntity(res.Insert)
	if err != nil {
		return nil, nil, techCardConvertErr(err)
	}
	if err := validateStyleNumberOverride(card); err != nil {
		return nil, nil, err
	}
	markers, err := tcciMarkers(res.MarkerPlan)
	if err != nil {
		return nil, nil, status.Errorf(codes.InvalidArgument, "this archive's %v", err)
	}
	// Server-stamped audit trail (norm §2.11), exactly as on the create path: the archive's own
	// created_by/updated_by travel as TEXT on the card and are a fact about the source. Who put this
	// card in THIS base is the person who pressed import.
	username := authsrv.GetAdminUsername(ctx)
	card.CreatedBy, card.UpdatedBy = username, username
	return card, markers, nil
}

// tcciWireGates runs the capability shields in the order CreateTechCard runs them.
//
// A list rather than nine hand-written calls, because the property that matters is «all of them»
// and a list is the shape in which a forgotten one is visible. The order is CreateTechCard's: the
// wire gates first (they speak the sentence a stale bundle needs — «update the admin panel» —
// before any field-level complaint), the stored gates after.
func tcciWireGates(in *pb_common.TechCardInsert) error {
	for _, gate := range []func(*pb_common.TechCardInsert) error{
		machineCapabilityWireGate,
		mediaCapabilityWireGate,
		assemblyCapabilityWireGate,
		operationKindsWireGate,
		operationWorkWireGate,
		bomQtyWireGate,
	} {
		if err := gate(in); err != nil {
			return err
		}
	}
	for _, gate := range []func(*pb_common.TechCardInsert, *entity.TechCard) error{
		mediaCapabilityStoredGate,
		assemblyCapabilityStoredGate,
		operationWorkRetiredGate,
	} {
		if err := gate(in, nil); err != nil {
			return err
		}
	}
	return nil
}

// tcciMarkers converts the resolver's раскладки into the store's payload.
//
// THROUGH THE WIRE CONVERTER, not by hand. dto.ConvertPbTechCardMarkerInsertToEntity is where a
// marker's form is decided — the decimal scales, the positive width, the numerator agreeing with
// the blob it describes — and a second transcription here would be a second opinion about what a
// marker is. The archive carries the READ shape (summary + layout), so the summary is folded back
// into the WRITE shape first; every field of that fold is a field the export wrote out of the same
// row.
//
// A MARKER THAT DOES NOT CONVERT FAILS THE IMPORT rather than becoming a hole, and that is the same
// answer the store gives two layers down (a duplicate name, a count over its total and a BOM key
// naming nothing are all field violations that abort the transaction). Our own export cannot
// produce one, so a refusal here means the file was made by something else — which is the corrupt
// archive case, not the missing-reference case.
func tcciMarkers(plans []tcimpMarkerPlan) ([]entity.TechCardMarkerInsert, error) {
	if len(plans) == 0 {
		return nil, nil
	}
	out := make([]entity.TechCardMarkerInsert, 0, len(plans))
	for _, p := range plans {
		ins, err := tcciMarkerInsert(p)
		if err != nil {
			return nil, fmt.Errorf("marker %q does not read as one: %w", p.Name, err)
		}
		out = append(out, ins)
	}
	return out, nil
}

// tcciMarkerInsert folds ONE archived marker back into the writable shape and marshals its geometry.
//
// The ceilings are SaveTechCardMarker's own, applied before the re-marshal for the reason that path
// states: the byte cap measures the output, and without the cardinality guards a file could ship
// tens of megabytes of points through this server to be parsed and re-marshalled first. They are
// the same numbers because they are the same question — what this instance is willing to store as
// one layout — and an import that accepted more would store a marker no save could ever rewrite.
func tcciMarkerInsert(p tcimpMarkerPlan) (entity.TechCardMarkerInsert, error) {
	var zero entity.TechCardMarkerInsert
	sum, layout := p.Marker.GetSummary(), p.Marker.GetLayout()
	if sum == nil {
		return zero, fmt.Errorf("it carries no summary")
	}
	if layout == nil || len(layout.GetPieces()) == 0 || len(layout.GetPlacements()) == 0 {
		return zero, fmt.Errorf("it carries no layout with pieces and placements")
	}
	if len(layout.GetPieces()) > maxMarkerPieces || len(layout.GetPlacements()) > maxMarkerPlacements {
		return zero, fmt.Errorf("its layout is too large: max %d pieces / %d placements",
			maxMarkerPieces, maxMarkerPlacements)
	}
	points := 0
	for _, piece := range layout.GetPieces() {
		points += len(piece.GetPoly())
	}
	if points > maxMarkerContourPoints {
		return zero, fmt.Errorf("its layout carries %d contour points, max %d", points, maxMarkerContourPoints)
	}
	if v := layout.GetSchemaVersion(); v < 1 || v > maxMarkerLayoutSchema {
		return zero, fmt.Errorf("its layout declares schema_version %d, which this server does not read (1..%d)",
			v, maxMarkerLayoutSchema)
	}

	// The INDEX is the authority on the name and the cloth link (FORMAT.md §5.7 gives markers/
	// index.json that role); the summary is the fallback for an archive whose index is thin.
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = strings.TrimSpace(sum.GetName())
	}
	bomLineKey := strings.TrimSpace(p.BomLineKey)
	if bomLineKey == "" {
		bomLineKey = strings.TrimSpace(sum.GetBomLineKey())
	}

	ins, err := dto.ConvertPbTechCardMarkerInsertToEntity(&pb_common.TechCardMarkerInsert{
		SizeId:          sum.GetSizeId(),
		Name:            name,
		Source:          sum.GetSource(),
		BomLineKey:      bomLineKey,
		FabricWidthCm:   sum.GetFabricWidthCm(),
		GapCm:           sum.GetGapCm(),
		EdgeMarginCm:    sum.GetEdgeMarginCm(),
		AllowCrossGrain: sum.GetAllowCrossGrain(),
		Sets:            sum.GetSets(),
		UsedLengthCm:    sum.GetUsedLengthCm(),
		EfficiencyPct:   sum.GetEfficiencyPct(),
		PlacedCount:     sum.GetPlacedCount(),
		TotalCount:      sum.GetTotalCount(),
		Layout:          layout,
		SelvedgeCm:      sum.GetSelvedgeCm(),
		// ZERO, NOT the summary's: an import creates no products, so there is no colourway here for
		// a раскладка to be pinned to. The resolver already zeroed it and reported the loss; this is
		// the same statement made where the payload is built, so a resolver that stopped doing it
		// could not smuggle a stranger's product id into a foreign key.
		ColorwayId: 0,
		// Only card markers travel (FORMAT.md §5.7) and the resolver skips any that claim a run.
		ProductionRunId:    0,
		SeamAllowanceMm:    sum.GetSeamAllowanceMm(),
		ContourAllowanceMm: sum.GetContourAllowanceMm(),
		ContourLayer:       sum.ContourLayer,
		GrainLayer:         sum.GrainLayer,
		AllowFlip:          sum.AllowFlip,
		// CONSENT to store a partially laid раскладка, and an import has no client to ask for it. A
		// source keeps such a marker quite happily, and refusing it here would abort a whole import
		// over geometry that already exists somewhere. The stored column is derived from the
		// counters by the store either way, so this consent describes nothing — it only decides
		// whether the payload is accepted.
		IsDraft: true,
	})
	if err != nil {
		return zero, err
	}

	blob, err := protojson.Marshal(layout)
	if err != nil {
		return zero, fmt.Errorf("its layout does not marshal: %w", err)
	}
	if len(blob) > maxMarkerLayoutBytes {
		return zero, fmt.Errorf("its layout is %d bytes, max %d", len(blob), maxMarkerLayoutBytes)
	}
	ins.Layout = string(blob)
	return ins, nil
}

// ────────────────────────────── the write, and what a failed one means ──────────────────────────

// tcciCommit is one commit in flight: everything the write needs, gathered so the retry below does
// not have to be handed eleven parameters.
type tcciCommit struct {
	importID string
	arch     *techcardarchive.Archive
	res      *resolvedTechCardImport
	place    *tcflPlacement
	card     *entity.TechCardInsert
	markers  []entity.TechCardMarkerInsert
	// actor is the importing operator, kept because a repair that re-derives the payload has to
	// re-stamp it: a rebuilt card carrying the archive's created_by would credit the import to
	// somebody in another company.
	actor string
	// holes are the lines this HANDLER added on top of the resolver's — a style number that turned
	// out to be taken, a reused picture that disappeared. They are kept apart from res.Holes so a
	// retry rebuilds the report from one accumulating list rather than mutating the resolver's.
	holes []techcardarchive.ImportHole
	// renumbered / remedied make each repair once. A repair that could fire twice is a loop: the
	// second style number can be taken too, and «try another one forever» is how an import spends an
	// afternoon writing nothing.
	renumbered bool
	remedied   bool
}

// tcciWrite runs the transaction, and repairs at most two things that can only be discovered by
// running it.
//
// BOTH REPAIRS RE-RUN THE WHOLE TRANSACTION AND NEITHER RE-UPLOADS A FILE. The bytes moved once,
// before the first attempt, and the remaps are in hand; a failed transaction rolled its own writes
// back and left the bucket alone. Re-placing the files would mint a second media row for every
// picture and orphan the first.
//
// The store deliberately does NOT retry either of them itself: a retry inside a SERIALIZABLE
// transaction would re-run every write above the failure, so the decision belongs out here where
// the transaction can simply be started again.
func (s *Server) tcciWrite(ctx context.Context, c tcciCommit) (*pb_admin.CommitTechCardImportResponse, error) {
	for {
		reportJSON, err := tcciReport(c)
		if err != nil {
			c.place.compensate(ctx, s)
			slog.Default().ErrorContext(ctx, "tech card import: can't encode the import report",
				slog.String("import_id", c.importID), slog.String("err", err.Error()))
			return nil, status.Error(codes.Internal, "can't commit the import")
		}

		id, err := s.repo.TechCards().ImportTechCardArchive(ctx, entity.TechCardArchiveImport{
			ImportID:          c.importID,
			Actor:             c.actor,
			SourceStyleNumber: c.arch.Manifest.Source.StyleNumber,
			SourceHost:        c.arch.Manifest.Source.Host,
			Card:              c.card,
			// STRAIGHT FROM THE RESOLVER, never re-read from the archive: Archive.CardJSON() parses
			// the ZIP entry afresh on every call, so a second read would hand back an UNSANITISED
			// message — approvals intact, prices intact, every id the source's — that merely looks
			// like the one everything above worked on.
			Style:      c.res.StylePlan,
			SizeChart:  c.res.SizeChartPlan,
			Assembly:   c.res.AssemblyPlan,
			Markers:    c.markers,
			PieceAreas: c.res.PieceAreaPlan,
			Labels:     tcciLabelLinks(c.res.LabelPlan),
			Report:     reportJSON,
		})
		if err == nil {
			return s.tcciCommitted(ctx, c.importID, id)
		}

		// «Somebody else committed this import» is not a fault to be classified and repaired: there
		// is nothing wrong with the payload, and running it again would only lose the same race a
		// second time. It goes straight to the settlement, which names the winner's card.
		if !errors.Is(err, entity.ErrImportAlreadyCommitted) {
			if repaired, rerr := s.tcciRepair(ctx, &c, err); rerr != nil {
				// A repair only ever runs on a DEFINITE rollback (a duplicate key and a foreign key
				// are raised by a statement), so a repair that itself fails leaves nothing written
				// and the files it moved are ours to take back. Compensated here rather than by
				// falling through to the settlement, which would answer with the store's error
				// instead of the repair's — the one a person can act on.
				c.place.compensate(ctx, s)
				return nil, rerr
			} else if repaired {
				continue
			}
		}
		return s.tcciSettleFailure(ctx, c, err)
	}
}

// tcciReport builds the report for THIS attempt.
//
// Rebuilt per attempt and not once, because two of its fields are outcomes rather than requests:
// style_number and stage are «as the card landed HERE», and a renumbered attempt lands under a
// different number than the one the archive asked for. A report carrying the archive's number next
// to a card carrying another one is the exact lie the proto's comment on those fields forbids.
//
// It is stamped by the STORE, inside the transaction, after the write has amended it with its own
// losses. Nothing here writes it anywhere.
func tcciReport(c tcciCommit) ([]byte, error) {
	holes := make([]techcardarchive.ImportHole, 0, len(c.res.Holes)+len(c.holes))
	holes = append(holes, c.res.Holes...)
	holes = append(holes, c.holes...)
	return techcardarchive.MarshalReport(techcardarchive.BuildReport(techcardarchive.ReportInput{
		ImportID:    c.importID,
		StyleNumber: c.card.StyleNumber.String,
		Stage:       string(c.card.Stage),
		Counters:    c.res.Counters,
		Holes:       holes,
		ExportHoles: c.arch.Manifest.ExportHoles,
	}))
}

// tcciLabelLinks carries the label → BOM line re-sew across the package boundary.
//
// Two identical structs rather than one shared type for the reason the export side gives about its
// own pair: package entity must not learn what a resolver is, and the resolver's type is private to
// package admin. The copy is four lines and the compiler checks it.
func tcciLabelLinks(in []tcimpLabelLink) []entity.TechCardArchiveLabelLink {
	if len(in) == 0 {
		return nil
	}
	out := make([]entity.TechCardArchiveLabelLink, 0, len(in))
	for _, l := range in {
		out = append(out, entity.TechCardArchiveLabelLink{LabelIndex: l.LabelIndex, BomLineKey: l.BomLineKey})
	}
	return out
}

// tcciRepair decides whether a failed transaction is worth running again, and prepares the payload
// for the second run. It returns (true, nil) when the caller should retry.
//
// EVERY BRANCH HERE STANDS ON A DEFINITE ROLLBACK. A duplicate key and a foreign key are raised by
// a STATEMENT, so the transaction that carried them is gone and nothing it wrote survives — which
// is why these two may retry while the ambiguous class below may not.
func (s *Server) tcciRepair(ctx context.Context, c *tcciCommit, err error) (bool, error) {
	switch {
	// ── the style number was taken between the dry run and now ──
	case !c.renumbered && s.repo.IsErrUniqueViolation(err):
		// A 1062 does not say WHICH unique index fired — the driver reports one number for every
		// index in the schema — and a card write touches two of them (style_number and the
		// equipment profile key of 0306). Renumbering on the strength of the number alone would
		// loop forever on the other one, minting a fresh style number on every pass. So the
		// conflict is CONFIRMED by reading the number back before anything is renamed.
		taken, lookupErr := s.tcciStyleNumberTaken(ctx, c.card.StyleNumber.String)
		if lookupErr != nil {
			slog.Default().ErrorContext(ctx, "tech card import: can't check whether the style number is taken",
				slog.String("import_id", c.importID),
				slog.String("style_number", c.card.StyleNumber.String),
				slog.String("err", lookupErr.Error()))
			return false, nil // fall through to the ordinary failure path; the files are settled there
		}
		if !taken {
			return false, nil // somebody else's unique index — not ours to rename around
		}
		c.holes = append(c.holes, s.tcciRenumber(ctx, c.card))
		c.renumbered = true
		return true, nil

	// ── a picture this base already held disappeared under us ──
	case !c.remedied && s.repo.IsErrForeignKeyViolation(err):
		holes := s.tcciDropVanishedMedia(ctx, c.res, c.place)
		if len(holes) == 0 {
			// The foreign key that broke was not a media row. Nothing here can repair it, and
			// pretending otherwise would retry an identical transaction.
			return false, nil
		}
		// THE REPAIR IS ON THE WIRE MESSAGE, AND THE PAYLOAD IS DERIVED FROM IT. Without this the
		// second attempt would carry the very id the first one broke on — the walk above would
		// have cleared the picture out of the insert while the entity built from that insert
		// twenty lines earlier still pointed at it, and the retry would fail identically and
		// silently. Re-derived through the same converter rather than patched by hand: the entity
		// tree references media in several places (card media, a step's pictures, a callout's
		// anchor) and a second «clear these ids» pass over it would be a second mechanism for one
		// rule.
		if err := c.rebuild(); err != nil {
			return false, err
		}
		c.holes = append(c.holes, holes...)
		c.remedied = true
		return true, nil
	}
	return false, nil
}

// rebuild re-derives the entity payload from the repaired insert and re-applies every decision this
// commit has already made about it — the operator's stamp, and a style number this handler picked.
//
// A decision is re-applied rather than re-taken: the number was chosen once, against the state of
// the base at that moment, and asking for another proposal here would mint a second number and
// leave the report describing the first.
func (c *tcciCommit) rebuild() error {
	fresh, err := dto.ConvertPbTechCardInsertToEntity(c.res.Insert)
	if err != nil {
		return techCardConvertErr(err)
	}
	fresh.CreatedBy, fresh.UpdatedBy = c.actor, c.actor
	if c.renumbered {
		fresh.StyleNumber = c.card.StyleNumber
		fresh.StyleNumberSource = c.card.StyleNumberSource
		fresh.Stage = c.card.Stage
	}
	c.card = fresh
	return nil
}

// tcciStyleNumberTaken reads the number back and reports whether a card here already carries it.
//
// POSITIVE CONFIRMATION, not inference from an error code. See tcciRepair for why: without it a
// duplicate equipment-profile key would be answered by renaming the style, forever.
//
// The list filter matches the number as a substring of name-or-style_number, so the exact match is
// re-checked in Go; the walk is paged to the end for the reason auxiliaryStyleNumbers gives — a
// partial index would answer «free» about a number that is taken, and this answer decides whether a
// card gets renamed.
func (s *Server) tcciStyleNumberTaken(ctx context.Context, number string) (bool, error) {
	number = strings.TrimSpace(number)
	if number == "" {
		return false, nil
	}
	const page = 100
	for offset := 0; ; offset += page {
		cards, total, err := s.repo.TechCards().ListTechCards(ctx, page, offset, entity.Ascending,
			entity.TechCardListFilter{Name: number})
		if err != nil {
			return false, fmt.Errorf("look up style number %q: %w", number, err)
		}
		for i := range cards {
			if strings.EqualFold(strings.TrimSpace(cards[i].StyleNumber.String), number) {
				return true, nil
			}
		}
		if len(cards) == 0 || offset+len(cards) >= total {
			return false, nil
		}
	}
}

// tcciRenumber gives the card a number it can actually have, and returns the line that says so.
//
// TWO OUTCOMES, and the second one is not a failure:
//
//   - THE CARD HAS A SEASON → the server proposes the next free number of that season, exactly as
//     the «suggest» button does, and the card lands under it with source=generated. The archive's
//     number is in the report, so the operator can decide whether the collision was a coincidence
//     or two people importing the same garment.
//   - IT HAS NO SEASON → there is nothing to generate FROM, so the card lands with NO NUMBER and
//     stage `idea`. That pairing is forced by the contract, not chosen here: a style number is
//     required from `proto` onward, so a numberless card at any later stage is unwritable. `idea`
//     is the honest stage for «a card that still has to be named», and the report says what to do.
//
// A failed proposal falls into the second outcome rather than failing the import. The card and its
// whole content are already in hand at that point, and losing them over a number a person can type
// in five seconds would be the wrong trade.
func (s *Server) tcciRenumber(ctx context.Context, card *entity.TechCardInsert) techcardarchive.ImportHole {
	was := strings.TrimSpace(card.StyleNumber.String)
	ref := fmt.Sprintf("style_number=%s", was)

	season := strings.TrimSpace(card.SeasonCode.String)
	if card.SeasonCode.Valid && season != "" && card.SeasonYear.Valid {
		next, err := s.repo.TechCards().SuggestStyleNumber(ctx, season, int(card.SeasonYear.Int32))
		if err != nil {
			slog.Default().WarnContext(ctx, "tech card import: can't propose a replacement style number",
				slog.String("season", season), slog.Int("year", int(card.SeasonYear.Int32)),
				slog.String("err", err.Error()))
		} else if n := strings.TrimSpace(next); n != "" {
			card.StyleNumber = sql.NullString{String: n, Valid: true}
			card.StyleNumberSource = entity.StyleNumberSourceGenerated
			return techcardarchive.ImportHole{
				Entity: techcardarchive.EntityCard, Ref: ref,
				Status: techcardarchive.StatusDegraded,
				Reason: techcardarchive.ReasonStyleNumberTaken,
				Detail: fmt.Sprintf("the archive's style number %q is already used by a card in this base, "+
					"so this one landed as %q", was, n),
			}
		}
	}

	card.StyleNumber = sql.NullString{}
	// GENERATED, not manual: nobody typed this absence, the server chose it. `manual` additionally
	// means «the value was hand-set and passed the strict validator», which an empty string is not.
	card.StyleNumberSource = entity.StyleNumberSourceGenerated
	card.Stage = entity.TechCardStageIdea
	return techcardarchive.ImportHole{
		Entity: techcardarchive.EntityCard, Ref: ref,
		Status: techcardarchive.StatusDegraded,
		Reason: techcardarchive.ReasonStyleNumberTaken,
		Detail: fmt.Sprintf("the archive's style number %q is already used here, and the card carries no "+
			"season to propose a replacement from, so it landed WITHOUT a number at stage `idea` — "+
			"give it a number to move it on", was),
	}
}

// tcciDropVanishedMedia takes the pictures that are no longer there out of the payload and reports
// them, so the import loses a picture instead of losing everything.
//
// THE WINDOW IT CLOSES. Ф3.1 mints a media row before this transaction opens, and the resolver of a
// PARALLEL import matches that row by content hash and plans to reuse it. If the first import then
// fails and compensates, the row is deleted while the second import is holding its id — and the
// second one's foreign key takes the whole card down. The owner's binding rule is that a reference
// that cannot be placed is a skip with a line in the report, never a refusal of the import, so the
// disappearance is repaired here and reported.
//
// The clearing runs through RemapIntFieldsDeep with an IDENTITY mapping of every media id that
// survives — the same walk and the same field list the resolver used to put those ids there. A
// bespoke «set these ids to zero» pass would be a second mechanism for one rule, and the two would
// drift the first time a media field is added to the contract.
func (s *Server) tcciDropVanishedMedia(ctx context.Context, res *resolvedTechCardImport, p *tcflPlacement) []techcardarchive.ImportHole {
	reused := map[int32]bool{}
	ids := []int{}
	for _, plan := range res.MediaPlan {
		if plan.Action == tcimpMediaReuse && plan.TargetID > 0 && !reused[plan.TargetID] {
			reused[plan.TargetID] = true
			ids = append(ids, int(plan.TargetID))
		}
	}
	if len(ids) == 0 {
		return nil
	}
	live, err := s.repo.Media().GetMediaByIds(ctx, ids)
	if err != nil {
		slog.Default().ErrorContext(ctx, "tech card import: can't re-check the pictures this base already held",
			slog.String("err", err.Error()))
		return nil
	}
	gone := map[int32]bool{}
	for id := range reused {
		if _, ok := live[int(id)]; !ok {
			gone[id] = true
		}
	}
	if len(gone) == 0 {
		return nil
	}

	// The mapping is every media id that STAYS, mapped to itself. Everything absent from it is
	// cleared by the walk — which is precisely the set that vanished, because after the resolver
	// and Ф3.1 the only media ids in this payload are reuse targets and freshly minted rows.
	mapping := make(map[int64]int64, len(reused)+len(p.mediaByPlaceholder))
	for id := range reused {
		if !gone[id] {
			mapping[int64(id)] = int64(id)
		}
	}
	for _, minted := range p.mediaByPlaceholder {
		mapping[int64(minted)] = int64(minted)
	}
	techcardarchive.RemapIntFieldsDeep(res.Insert.ProtoReflect(), techcardarchive.MediaFieldNames, mapping,
		func(field string, old int64) {
			slog.Default().WarnContext(ctx, "tech card import: a picture this base held disappeared mid-import",
				slog.String("field", field), slog.Int64("media_id", old))
		})
	// The same gesture the resolver and Ф3.1 both use for a slot whose picture is gone — one answer
	// in this package to what happens to a row with no media behind it.
	(&tcimpResolver{out: res}).dropEmptyMediaRows()

	holes := make([]techcardarchive.ImportHole, 0, len(gone))
	for _, plan := range res.MediaPlan {
		if plan.Action != tcimpMediaReuse || !gone[plan.TargetID] {
			continue
		}
		res.Counters.AddImported(techcardarchive.EntityMedia, -1)
		res.Counters.AddSkipped(techcardarchive.EntityMedia, 1)
		holes = append(holes, techcardarchive.ImportHole{
			Entity: techcardarchive.EntityMedia,
			Ref:    fmt.Sprintf("media_id=%d", plan.SourceID),
			Status: techcardarchive.StatusSkipped,
			// The closest true code in a CLOSED dictionary, and the right one for the operator: its
			// action is «import the same archive again», which is exactly what fixes this. The
			// detail carries what actually happened, because the code cannot.
			Reason: techcardarchive.ReasonMediaUploadFailed,
			Detail: "this picture matched one this base already stored, and that stored picture was deleted " +
				"while the import was running — most likely by another import being taken back. The slot " +
				"was left empty; importing the archive again will upload the file afresh",
		})
	}
	return holes
}

// ────────────────────────────── settling a commit nobody can see the end of ─────────────────────

// tcciVerdict is what a fresh read of tech_card_import says about a commit that returned an error.
type tcciVerdict int

const (
	// tcciVerdictRolledBack — the row is still `uploaded`: the transaction that claimed it is gone and
	// nothing it wrote survives. The files this import moved are orphans and may be taken back.
	tcciVerdictRolledBack tcciVerdict = iota
	// tcciVerdictCommitted — the row is `committed` and carries a card id. Both are written by the SAME
	// transaction (the claim at its first statement, the id at its last), so seeing either on a
	// fresh connection is proof that the whole thing committed. The error was on the way back.
	tcciVerdictCommitted
	// tcciVerdictUnknown — the state could not be established. NOTHING IS COMPENSATED on this verdict:
	// see tcciSettleFailure.
	tcciVerdictUnknown
)

// tcciSettlement is the verdict plus the card id, when there is one.
type tcciSettlement struct {
	verdict    tcciVerdict
	techCardID int
}

// tcciSettle re-reads the import row on a FRESH connection and says what actually happened.
//
// WHY THIS EXISTS AT ALL: an error out of the transaction wrapper does not distinguish «the
// callback failed» from «COMMIT failed». The second one can mean the server applied the commit and
// the acknowledgement was lost — the card is alive and its pattern objects are referenced by rows
// that have no foreign key to protect them. Compensating on that error would delete a live card's
// sheets and leave a card of four-hundred-and-fours.
//
// THE CONTEXT IS DETACHED. The commonest reason to be settling is a context that has just died, and
// a settlement that inherits it can only ever answer «unknown» — which forbids compensation
// forever and turns every cancelled request into permanent orphans. Values ride along; the deadline
// and the cancellation do not.
//
// A read error is `unknown`, and so is a row that has gone missing: both mean «this cannot be
// established», and the whole point of the verdict is that only a POSITIVE «rolled back» unlocks
// deletion.
func (s *Server) tcciSettle(ctx context.Context, importID string) tcciSettlement {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tcciSettleTimeout)
	defer cancel()

	row, err := s.repo.TechCards().GetTechCardImportByImportID(sctx, importID)
	if err != nil {
		slog.Default().ErrorContext(sctx, "tech card import: can't establish whether the commit landed",
			slog.String("import_id", importID), slog.String("err", err.Error()))
		return tcciSettlement{verdict: tcciVerdictUnknown}
	}
	if row.Status != entity.TechCardImportStatusCommitted {
		return tcciSettlement{verdict: tcciVerdictRolledBack}
	}
	if row.TechCardID.Valid && row.TechCardID.Int32 > 0 {
		return tcciSettlement{verdict: tcciVerdictCommitted, techCardID: int(row.TechCardID.Int32)}
	}
	// `committed` with no card id cannot be produced by one transaction — the claim and the stamp
	// are two statements of the same one. Something else wrote this row, and guessing is exactly
	// what must not happen next.
	return tcciSettlement{verdict: tcciVerdictUnknown}
}

// tcciSettleFailure is the last word on a commit that returned an error: what to tell the operator,
// and whether the files may be taken back.
//
// THE RULE, in one line: compensation requires PROOF that nothing was written.
//
//   - already committed → definite. The claim never matched, so this call wrote nothing at all; the
//     files are ours to take back, and the answer names the card the winner made.
//   - rolled back → definite. Take the files back and say the import failed.
//   - committed → the import LANDED and the error was on the way back. Answer with the card.
//   - unknown → say nothing was deleted. An orphaned object costs storage until the sweeper (Ф5.1)
//     finds it; an object deleted out from under a live card costs the card.
func (s *Server) tcciSettleFailure(ctx context.Context, c tcciCommit, err error) (
	*pb_admin.CommitTechCardImportResponse, error) {

	if errors.Is(err, entity.ErrImportAlreadyCommitted) {
		// The store's own claim refused this call INSIDE its transaction, so nothing of ours is in
		// the database and the files it moved belong to nobody.
		c.place.compensate(ctx, s)
		settled := s.tcciSettle(ctx, c.importID)
		row := entity.TechCardArchiveImportRecord{ImportID: c.importID}
		if settled.verdict == tcciVerdictCommitted {
			row.TechCardID = sql.NullInt32{Int32: int32(settled.techCardID), Valid: true}
		}
		return nil, tcciAlreadyCommitted(row)
	}

	settled := s.tcciSettle(ctx, c.importID)
	switch settled.verdict {
	case tcciVerdictCommitted:
		slog.Default().WarnContext(ctx, "tech card import: the commit landed but reported an error",
			slog.String("import_id", c.importID), slog.Int("tech_card_id", settled.techCardID),
			slog.String("err", err.Error()))
		return s.tcciCommitted(ctx, c.importID, settled.techCardID)

	case tcciVerdictRolledBack:
		slog.Default().ErrorContext(ctx, "tech card import: the commit failed and was rolled back",
			slog.String("import_id", c.importID), slog.String("err", err.Error()))
		c.place.compensate(ctx, s)

	default: // tcciVerdictUnknown
		slog.Default().ErrorContext(ctx,
			"tech card import: the commit's outcome could not be established; the files it moved were LEFT IN PLACE",
			slog.String("import_id", c.importID),
			slog.Any("objects", c.place.uploadedKeys()),
			slog.String("err", err.Error()))
	}

	// A field violation out of the store is about the ARCHIVE's content — a marker naming a BOM line
	// the archive did not carry, two markers with one name — and the operator can act on it.
	// Everything else is ours.
	var ve *entity.ValidationError
	if errors.As(err, &ve) {
		return nil, apierr.Invalid(ve)
	}
	return nil, status.Errorf(codes.Internal,
		"the import could not be written and no card was created; try again (import %s)", c.importID)
}

// tcciCommitted builds the success answer, with THE REPORT THAT WAS STORED.
//
// Read back rather than re-encoded from what this handler built, because the two are not the same
// document: the transaction amends the report with what the WRITE dropped — chart cells outside the
// imported card's own size range, a grade base whose size did not survive, measured areas, an
// assembly line whose component turned out to be something else here — and only then stamps it.
// Answering with the pre-transaction copy would show an operator a report that counts those rows as
// imported. They read it once, believe it, and never look at the card again.
//
// IF THE READ-BACK FAILS THE IMPORT IS STILL A SUCCESS, and the answer carries the card id with NO
// report rather than the stale one. The card exists; failing the RPC now would tell the operator
// their import failed while a card of theirs sits in the catalogue, and a second press would only
// answer «already imported». The report is not lost either — it is on the card, which is where it
// lives from now on (GetTechCardImportReport).
func (s *Server) tcciCommitted(ctx context.Context, importID string, techCardID int) (
	*pb_admin.CommitTechCardImportResponse, error) {

	out := &pb_admin.CommitTechCardImportResponse{TechCardId: int32(techCardID)}

	row, err := s.repo.TechCards().GetTechCardImportByImportID(ctx, importID)
	switch {
	case err != nil:
		slog.Default().ErrorContext(ctx, "tech card import: committed, but the stored report could not be read back",
			slog.String("import_id", importID), slog.Int("tech_card_id", techCardID),
			slog.String("err", err.Error()))
	case len(row.Report) == 0:
		slog.Default().ErrorContext(ctx, "tech card import: committed, but the row carries no report",
			slog.String("import_id", importID), slog.Int("tech_card_id", techCardID))
	default:
		stored := &pb_admin.TechCardImportReport{}
		if uerr := protojson.Unmarshal(row.Report, stored); uerr != nil {
			slog.Default().ErrorContext(ctx, "tech card import: the stored report does not read as one",
				slog.String("import_id", importID), slog.Int("tech_card_id", techCardID),
				slog.String("err", uerr.Error()))
		} else {
			out.Report = stored
		}
	}

	slog.Default().InfoContext(ctx, "tech card archive imported",
		slog.String("import_id", importID), slog.Int("tech_card_id", techCardID),
		slog.String("style_number", out.GetReport().GetStyleNumber()),
		slog.Int("report_lines", len(out.GetReport().GetLines())))
	return out, nil
}
