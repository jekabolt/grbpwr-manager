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

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
