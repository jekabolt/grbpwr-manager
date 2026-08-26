package admin

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/encoding/protojson"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф2.5 — THE DOOR AN IMPORT COMES THROUGH: POST /api/techcard-archive/upload.
//
// A ZIP arrives over HTTP, lands in a private bucket object, is read, resolved against THIS base,
// and the operator gets a report — with nothing written to the card tables and no card created.
// The only two things this route leaves behind are the object and one row of tech_card_import; the
// card is made later, by an explicit second call, after a human has read the report.
//
// THREE PROPERTIES THIS FILE EXISTS TO HOLD:
//
//   - A REFUSAL IS A REFUSAL, NEVER A TRIM. The body ceiling is 256 MiB (FORMAT.md §1.3) and it is
//     enforced twice — by the route's own MaxBytesReader and again inside UploadImportObject, whose
//     capReader errors rather than delivering EOF. A silently truncated ZIP is indistinguishable
//     from a corrupt one at the point where the operator looks, and both of those are
//     indistinguishable from a clean short archive.
//
//   - THE DRY RUN IS HONEST ABOUT ITSELF. The report handed back is built by the SAME pipeline the
//     commit runs — resolve → BuildReport → ValidateReportAgainstManifest — and it is not cached:
//     the commit re-opens the archive and re-resolves, because the base can move between the two
//     calls. What a preview cannot promise is named rather than implied: the two classes of line
//     only the commit can produce are a file the target bucket refuses (media_upload_failed) and a
//     style number taken between now and then (style_number_taken). Everything else the operator
//     reads here is what the commit will find. See tcupPreviewStage for the one field that is a
//     REQUEST rather than an outcome.
//
//   - THE MANIFEST IS STORED AS BYTES, NOT AS A STRUCT. archive_manifest gets Archive.ManifestRaw —
//     manifest.json verbatim. A 1.x archive parsed into the 1.0 struct and re-marshalled loses
//     every field this server has no member for, silently, under the label "what was in the ZIP at
//     upload" (FORMAT.md §3).
//
// RBAC IS CHECKED HERE, BY HAND, BEFORE THE BODY IS TOUCHED. The rbac method map covers gRPC
// methods; a plain HTTP route never meets the interceptor that reads it, so the check lives in the
// handler and fails closed — exactly the posture of the files-library upload next door.
// ─────────────────────────────────────────────────────────────────────────────

const (
	// tcupPartArchive is the multipart part carrying the .zip. "file" is accepted as well because
	// that is what a browser's <input type=file> is named by default in half the code that will ever
	// call this; naming both here is cheaper than a support conversation about a 400.
	tcupPartArchive = "archive"
	tcupPartFile    = "file"

	// tcupImportIDLen is CHAR(26) — the width of tech_card_import.import_id and of every other
	// stable key this codebase mints (base32 of 128 random bits).
	tcupImportIDLen = 26
)

// tcupUploadResponse is what comes back: the id the commit call will name, and the dry-run report.
//
// dry_run is not decoration. This body is shaped exactly like the commit's response minus the card
// id, and a client that keeps both in one cache has to be able to tell "what would happen" from
// "what happened" without inferring it from an absent field.
type tcupUploadResponse struct {
	ImportID string `json:"import_id"`
	DryRun   bool   `json:"dry_run"`
	// Report is protojson of pb_admin.TechCardImportReport — camelCase with EmitUnpopulated, the
	// byte shape the gateway produces for every other admin response, because the client feeds it to
	// the same generated type.
	Report json.RawMessage `json:"report"`
}

// TechCardArchiveUploadHandler accepts one tech-card ZIP and answers with the dry-run report.
//
// Mounted at POST /api/techcard-archive/upload, OUTSIDE the gRPC gateway: a 256 MiB archive cannot
// ride inside a gRPC message. The caller wraps it in admin authorization (app wiring) — that
// wrapping authenticates, and the section check below decides what this route requires.
func (s *Server) TechCardArchiveUploadHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		started := time.Now()
		defer r.Body.Close()

		// FAIL CLOSED, AND BEFORE READING A BYTE. wr(tech_cards) rather than the reader's right: an
		// import creates a card, and it does it out of a file whose provenance this server cannot
		// check. An absent authorization has no permissions at all.
		authz, ok := authsrv.GetAdminAuthz(ctx)
		if !ok || !(authz.FullAccess() || authz.Perms[rbac.SectionTechCards].Covers(entity.AccessWrite)) {
			writeUploadError(w, http.StatusForbidden, "tech_cards:write is required to import a tech card archive")
			return
		}
		username := authsrv.GetAdminUsername(ctx)

		mr, err := r.MultipartReader()
		if err != nil {
			writeUploadError(w, http.StatusBadRequest,
				`request must be multipart/form-data with the .zip in an "archive" part`)
			return
		}
		part, err := mr.NextPart()
		if err != nil {
			writeUploadError(w, http.StatusBadRequest, `expected an "archive" part carrying the .zip`)
			return
		}
		if name := part.FormName(); name != tcupPartArchive && name != tcupPartFile {
			writeUploadError(w, http.StatusBadRequest,
				fmt.Sprintf(`the first part must be %q, got %q`, tcupPartArchive, name))
			return
		}

		importID, err := tcupMintImportID()
		if err != nil {
			slog.Default().ErrorContext(ctx, "tech card import: can't mint an import id",
				slog.String("username", username), slog.String("err", err.Error()))
			writeUploadError(w, http.StatusInternalServerError, "could not accept the archive")
			return
		}

		// Straight from the socket into the bucket. The archive is never buffered: a card with a
		// hundred megabytes of video is a legal card, and holding one in memory to learn its length
		// would turn one import into an outage.
		objectKey, err := s.bucket.UploadImportObject(ctx, part, importID)
		if err != nil {
			tcupWriteUploadFailure(ctx, w, username, importID, err)
			return
		}

		// From here on the object exists and nothing points at it yet, so every failure path below
		// removes it. The row is the only thing that makes an object owned.
		report, manifestRaw, colorwaysRaw, ok := s.tcupDryRun(ctx, w, importID, objectKey)
		if !ok {
			cleanupObjects(ctx, s.bucket, objectKey)
			return
		}

		if err := s.repo.TechCards().CreateTechCardImportRow(ctx, importID, objectKey,
			manifestRaw, colorwaysRaw, username); err != nil {
			cleanupObjects(ctx, s.bucket, objectKey)
			slog.Default().ErrorContext(ctx, "tech card import: can't record the import row",
				slog.String("username", username), slog.String("import_id", importID),
				slog.String("object_key", objectKey), slog.String("err", err.Error()))
			writeUploadError(w, http.StatusInternalServerError, "the archive was read but could not be recorded")
			return
		}

		reportJSON, err := protojson.MarshalOptions{EmitUnpopulated: true, UseProtoNames: false}.Marshal(report)
		if err != nil {
			// The row is written and the object is owned by it: the import is alive and the commit
			// call can still be made. Only this response is lost, so nothing is cleaned up here.
			slog.Default().ErrorContext(ctx, "tech card import: can't marshal the dry-run report",
				slog.String("import_id", importID), slog.String("err", err.Error()))
			writeUploadError(w, http.StatusInternalServerError, "could not encode the report")
			return
		}

		slog.Default().InfoContext(ctx, "tech card archive uploaded for import",
			slog.String("username", username),
			slog.String("import_id", importID),
			slog.String("object_key", objectKey),
			slog.String("style_number", report.GetStyleNumber()),
			slog.Int("report_lines", len(report.GetLines())),
			slog.Duration("took", time.Since(started)))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(tcupUploadResponse{
			ImportID: importID,
			DryRun:   true,
			Report:   reportJSON,
		}); err != nil {
			slog.Default().ErrorContext(ctx, "tech card import: can't write the upload response",
				slog.String("import_id", importID), slog.String("err", err.Error()))
		}
	})
}

// tcupDryRun reads the uploaded object and produces the report, writing NOTHING. ok=false means the
// answer has already been written to w and the caller only has to clean up the object.
//
// It returns the manifest and colourway bytes alongside the report rather than letting the caller
// re-read them: they are the archive's own bytes, and a second read is a second chance to get a
// re-marshal in by accident.
func (s *Server) tcupDryRun(ctx context.Context, w http.ResponseWriter, importID, objectKey string) (
	report *pb_admin.TechCardImportReport, manifestRaw, colorwaysRaw []byte, ok bool) {

	ra, size, err := s.bucket.GetImportObjectReaderAt(ctx, objectKey)
	if err != nil {
		slog.Default().ErrorContext(ctx, "tech card import: can't read back the uploaded archive",
			slog.String("import_id", importID), slog.String("object_key", objectKey),
			slog.String("err", err.Error()))
		writeUploadError(w, http.StatusInternalServerError, "the archive was stored but could not be read back")
		return nil, nil, nil, false
	}
	defer ra.Close()

	arch, err := techcardarchive.OpenArchive(ra, size)
	if err != nil {
		tcupWriteArchiveError(ctx, w, importID, "read", err)
		return nil, nil, nil, false
	}

	// The resolver reads the target base and writes nowhere. Its errors are infrastructure or a
	// corrupt archive — never a missing reference, which degrades into a report line.
	res, err := s.resolveTechCardImport(ctx, arch)
	if err != nil {
		tcupWriteArchiveError(ctx, w, importID, "resolve", err)
		return nil, nil, nil, false
	}

	rep := techcardarchive.BuildReport(techcardarchive.ReportInput{
		ImportID:    importID,
		StyleNumber: res.Insert.GetStyleNumber(),
		Stage:       tcupPreviewStage(res.Insert.GetStage()),
		Counters:    res.Counters,
		Holes:       res.Holes,
		ExportHoles: arch.Manifest.ExportHoles,
	})

	// THE POSITIVE CONTROL, and it runs on the DRY RUN rather than only on the commit on purpose: an
	// empty report is what a dead parser and a clean archive look like from the outside, and the
	// screen that shows "nothing to worry about" is this one.
	if err := techcardarchive.ValidateReportAgainstManifest(rep, arch.Manifest); err != nil {
		slog.Default().ErrorContext(ctx, "tech card import: the report failed its positive control",
			slog.String("import_id", importID), slog.String("err", err.Error()))
		// NOT "did not parse" (R2-11). The file opened, the ZIP was walked and the manifest was
		// read — telling the operator it did not parse sends them hunting for a corrupt download,
		// which is the one thing this is not. What failed is the positive control: the archive's
		// own `contents` claim and what came out of the parse disagree. The words have to name
		// that CONTRADICTION, because it is what the operator has to act on — either the manifest
		// overstates the archive, or the parse stopped halfway.
		writeUploadError(w, http.StatusBadRequest, "this archive contradicts itself: "+err.Error())
		return nil, nil, nil, false
	}

	return rep, arch.ManifestRaw, res.ColorwaysRaw, true
}

// tcupPreviewStage is the one report field that is a REQUEST rather than an outcome.
//
// The proto says style_number and stage are FINAL — as the card landed. Nothing has landed yet, so
// the preview says what the archive ASKS for, resolved through the same defaulting rule the create
// converter applies (an unset stage becomes proto). The commit may still land another stage: a
// style number already taken with no season to generate a replacement from forces `idea`, and that
// decision belongs to the write path, which is why it is not guessed at here.
func tcupPreviewStage(stage pb_common.TechCardStage) string {
	s, err := dto.ConvertPbTechCardStageToEntityString(stage)
	if err != nil || s == "" {
		return string(entity.TechCardStageProto)
	}
	return s
}

// tcupMintImportID mints the 26-character identity of one import: base32 of 128 random bits, the
// same shape as every other stable key here. It is key-safe by construction ([A-Z2-7] only), which
// matters because it goes INTO the bucket key.
func tcupMintImportID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("read randomness for a tech card import id: %w", err)
	}
	id := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:])
	if len(id) != tcupImportIDLen { // unreachable; guards a future encoder swap against CHAR(26)
		return "", fmt.Errorf("minted import id %q is %d characters, not %d", id, len(id), tcupImportIDLen)
	}
	return id, nil
}

// tcupWriteUploadFailure answers a failed stream-into-the-bucket, keeping the one distinction that
// matters to the person: TOO BIG is not BROKEN.
func tcupWriteUploadFailure(ctx context.Context, w http.ResponseWriter, username, importID string, err error) {
	if tcupIsTooLarge(err) {
		writeUploadError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("the archive is larger than the %d MiB this server accepts", techcardarchive.MaxUploadedArchiveBytes>>20))
		return
	}
	if isTruncatedBody(err) {
		// The upload was cut short. It must stay distinguishable from a corrupt archive: the bytes
		// that did arrive are a valid prefix of a valid ZIP and nothing downstream could tell.
		slog.Default().WarnContext(ctx, "tech card import: the upload was cut short",
			slog.String("username", username), slog.String("import_id", importID),
			slog.String("err", err.Error()))
		writeUploadError(w, http.StatusBadRequest,
			"the upload was cut short — the connection dropped before the whole archive arrived")
		return
	}
	if errors.Is(err, http.ErrHandlerTimeout) {
		writeUploadError(w, http.StatusRequestTimeout, "the upload timed out")
		return
	}
	slog.Default().ErrorContext(ctx, "tech card import: can't store the uploaded archive",
		slog.String("username", username), slog.String("import_id", importID),
		slog.String("err", err.Error()))
	writeUploadError(w, http.StatusInternalServerError, "could not store the archive")
}

// tcupIsTooLarge reports whether err means the caller sent more than the ceiling allows — from the
// route's MaxBytesReader or from the bucket's own capReader, which are two enforcements of ONE
// number (techcardarchive.MaxUploadedArchiveBytes) and must produce one answer.
//
// The string check is not superstition: the reader error travels through minio's PutObject, and
// whether a %w survives that trip is a property of a dependency, not of this code. A ceiling that
// reports 500 instead of 413 sends the operator to look for a server fault they will not find.
func tcupIsTooLarge(err error) bool {
	if err == nil {
		return false
	}
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr) ||
		errors.Is(err, bucket.ErrArchiveObjectTooLarge) ||
		strings.Contains(err.Error(), "http: request body too large")
}

// tcupWriteArchiveError classifies what the reader and the resolver refuse. Their vocabulary is the
// whole of FORMAT.md §6.3: corruption, a foreign MAJOR and a missing money policy fail the archive
// (the caller's problem, 400); anything else came from this side (500).
//
// FAILS CLOSED THE OTHER WAY ROUND than IsFatal: an error class nobody here has heard of is treated
// as OURS, because blaming a person's file for a fault of our own is the more expensive mistake.
func tcupWriteArchiveError(ctx context.Context, w http.ResponseWriter, importID, phase string, err error) {
	if errors.Is(err, techcardarchive.ErrRefused) ||
		errors.Is(err, techcardarchive.ErrCorrupt) ||
		errors.Is(err, techcardarchive.ErrNotFound) {
		slog.Default().InfoContext(ctx, "tech card import: the archive was refused",
			slog.String("import_id", importID), slog.String("phase", phase),
			slog.String("err", err.Error()))
		writeUploadError(w, http.StatusBadRequest, err.Error())
		return
	}
	slog.Default().ErrorContext(ctx, "tech card import: can't "+phase+" the archive",
		slog.String("import_id", importID), slog.String("err", err.Error()))
	writeUploadError(w, http.StatusInternalServerError, "could not read the archive")
}
