package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	chi "github.com/go-chi/chi/v5"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф2.5 — the import door, tested for the four ways it can lie.
//
//  1. It lets somebody in who has no right to import (and reads their body while deciding).
//  2. It stores a manifest it re-marshalled, so the journal shows a SHORTER archive than arrived.
//  3. It answers 500 (a fault of ours, retry it) where the truth is 413 (too big, nothing to retry).
//  4. It answers 200 with an empty report because the parser produced nothing at all.
//
// Helpers here are prefixed tcup* — the package already owns `dec`, `tcz*`, `amg*` and `tcimp*`.
// The archives are built from RAW JSON on purpose rather than through the tcimp* fixture: this file
// has to be able to write a manifest field the 1.0 struct has no member for, which is precisely the
// thing a typed fixture cannot express.
// ─────────────────────────────────────────────────────────────────────────────

// tcupZip writes an honest ZIP with real bodies and real CRCs.
func tcupZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	return tcimpZip(t, files)
}

// tcupManifestJSON is manifest.json as TEXT. extra is spliced in verbatim, which is how a "newer
// MINOR" archive is written: fields this server has no member for, in the bytes that arrive.
func tcupManifestJSON(contents string, extra string) []byte {
	if contents == "" {
		contents = `{}`
	}
	if extra != "" && !strings.HasSuffix(extra, ",") {
		extra += ","
	}
	return []byte(fmt.Sprintf(`{
  "format": %q,
  "format_version": %q,
  "money_policy": %q,
  "exported_at": "2026-08-25T14:00:00Z",
  "exported_by": "im",
  %s
  "source": {"host": "backend.source.example", "tech_card_id": 214, "style_number": "GRB-SS26-014"},
  "id_maps": {"sizes": {"3": "s", "4": "m"}},
  "contents": %s,
  "export_holes": []
}`, techcardarchive.FormatName, techcardarchive.FormatVersion, techcardarchive.MoneyPolicyStrippedV1,
		extra, contents))
}

// tcupCardJSON is the mandatory card.json: the smallest card that resolves cleanly against the
// dictionary tcimpServer wires (sizes s/m/l).
func tcupCardJSON(t *testing.T) []byte {
	t.Helper()
	raw, err := protojson.Marshal(&pb_common.TechCard{
		Id: 214,
		TechCard: &pb_common.TechCardInsert{
			StyleNumber: "GRB-SS26-014",
			Name:        "coat",
			SizeIds:     []int32{3, 4},
			Stage:       pb_common.TechCardStage_TECH_CARD_STAGE_FIT,
		},
	})
	require.NoError(t, err)
	return raw
}

// tcupArchive builds a valid archive around the given manifest bytes.
func tcupArchive(t *testing.T, manifest []byte, extra map[string][]byte) []byte {
	t.Helper()
	files := map[string][]byte{
		techcardarchive.FileManifest: manifest,
		techcardarchive.FileCard:     tcupCardJSON(t),
	}
	for k, v := range extra {
		files[k] = v
	}
	return tcupZip(t, files)
}

// tcupBody wraps payload in a one-part multipart body.
func tcupBody(t *testing.T, partName string, payload []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile(partName, "techcard-GRB-SS26-014.zip")
	require.NoError(t, err)
	_, err = part.Write(payload)
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	return &buf, mw.FormDataContentType()
}

// tcupLimitBody mirrors internal/api/http's limitBody — the route middleware whose MaxBytesReader is
// what a body over the ceiling actually meets in production. Copied rather than imported because it
// is unexported there; what matters for this file is the ERROR SHAPE it produces, which is
// http.MaxBytesError either way.
func tcupLimitBody(max int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, max)
		next.ServeHTTP(w, r)
	})
}

// tcupServe drives the handler through a chi mux mounted at the production path. bodyCap > 0 puts
// the route's body ceiling in front of it.
func tcupServe(t *testing.T, s *Server, ctx context.Context, body io.Reader, contentType string, bodyCap int64) *httptest.ResponseRecorder {
	t.Helper()
	var h http.Handler = s.TechCardArchiveUploadHandler()
	if bodyCap > 0 {
		h = tcupLimitBody(bodyCap, h)
	}
	r := chi.NewRouter()
	r.Method(http.MethodPost, "/techcard-archive/upload", h)

	req := httptest.NewRequest(http.MethodPost, "/techcard-archive/upload", body).WithContext(ctx)
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// tcupWriterCtx is an authorization that MAY import: tech_cards:write, and a username the row has to
// carry.
func tcupWriterCtx() context.Context {
	return authsrv.PutAdminUsername(
		authsrv.PutAdminAuthz(context.Background(), authsrv.AdminAuthz{
			Perms: map[string]entity.AccessLevel{rbac.SectionTechCards: entity.AccessWrite},
		}), "im")
}

// tcupReaderAt is what the bucket hands back for zip.NewReader.
type tcupReaderAt struct{ *bytes.Reader }

func (tcupReaderAt) Close() error { return nil }

// tcupBucket wires the bucket round trip the handler performs: the archive is streamed into an
// object, then read back out of it. The uploaded bytes are captured and served back, so the test
// exercises the real path (upload → read back → open) rather than handing the reader a fixture the
// handler never stored.
func tcupBucket(t *testing.T, fs *mocks.MockFileStore) *[]byte {
	t.Helper()
	stored := new([]byte)
	fs.EXPECT().UploadImportObject(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, r io.Reader, importID string) (string, error) {
			raw, err := io.ReadAll(r)
			if err != nil {
				return "", err
			}
			*stored = raw
			return techcardarchive.BucketPrefixImports + importID + ".zip", nil
		}).Maybe()
	fs.EXPECT().GetImportObjectReaderAt(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, key string) (dependency.ReaderAtCloser, int64, error) {
			return tcupReaderAt{bytes.NewReader(*stored)}, int64(len(*stored)), nil
		}).Maybe()
	return stored
}

// tcupServer is a Server whose repo AND bucket are strict mocks: an unexpected call fails the test,
// so "the dry run wrote nothing else" is proved by the test being green at all.
func tcupServer(t *testing.T) (*Server, *mocks.MockTechCards, *mocks.MockFileStore) {
	t.Helper()
	s, _, cards, _ := tcimpServer(t)
	fs := mocks.NewMockFileStore(t)
	s.bucket = fs
	return s, cards, fs
}

// tcupImportRow is one captured CreateTechCardImportRow call.
type tcupImportRow struct {
	importID   string
	objectKey  string
	manifest   []byte
	colorways  []byte
	importedBy string
}

// tcupExpectRow captures the row the handler writes. The pointer is nil until it is written, which
// is what the "nothing was recorded" assertions read.
func tcupExpectRow(cards *mocks.MockTechCards) **tcupImportRow {
	got := new(*tcupImportRow)
	cards.EXPECT().CreateTechCardImportRow(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, importID, objectKey string, manifest, colorways []byte, importedBy string) error {
			*got = &tcupImportRow{
				importID: importID, objectKey: objectKey,
				manifest: manifest, colorways: colorways, importedBy: importedBy,
			}
			return nil
		}).Maybe()
	return got
}

// tcupDecode reads the success body.
func tcupDecode(t *testing.T, w *httptest.ResponseRecorder) (string, bool, *pb_admin.TechCardImportReport) {
	t.Helper()
	var body struct {
		ImportID string          `json:"import_id"`
		DryRun   bool            `json:"dry_run"`
		Report   json.RawMessage `json:"report"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), "response body: %s", w.Body.String())
	rep := &pb_admin.TechCardImportReport{}
	require.NoError(t, protojson.Unmarshal(body.Report, rep))
	return body.ImportID, body.DryRun, rep
}

// tcupErrorText reads the failure body's one word for the operator.
func tcupErrorText(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), "response body: %s", w.Body.String())
	return body.Error
}

// ────────────────────────────── 1. the right to import ──────────────────────────────

// tcupTripwireBody fails the test if anything reads it. The RBAC check has to happen BEFORE the
// body is touched: a route that streams a quarter of a gigabyte into the bucket and only then
// discovers the caller may not import has already done the expensive, side-effecting half of the
// work for somebody with no right to ask for it.
type tcupTripwireBody struct{ t *testing.T }

func (b *tcupTripwireBody) Read([]byte) (int, error) {
	b.t.Error("the request body was read before the authorization check")
	return 0, io.EOF
}

// TestTechcardArchiveUploadRequiresTechCardsWrite: the section check lives in the handler because
// the gRPC interceptor that guards every other admin write never sees a plain HTTP route. No
// authorization at all and a read-only one are both refused — and neither one gets to spend a byte
// of the server's attention on its body.
func TestTechcardArchiveUploadRequiresTechCardsWrite(t *testing.T) {
	// No repo and no bucket: if anything downstream ran, it would panic on a nil field, which is a
	// second, blunter proof that the refusal is complete.
	s := &Server{}

	if got := tcupServe(t, s, context.Background(), &tcupTripwireBody{t}, "multipart/form-data; boundary=x", 0).Code; got != http.StatusForbidden {
		t.Errorf("no authorization: want 403, got %d", got)
	}

	readOnly := authsrv.PutAdminAuthz(context.Background(), authsrv.AdminAuthz{
		Perms: map[string]entity.AccessLevel{rbac.SectionTechCards: entity.AccessRead},
	})
	if got := tcupServe(t, s, readOnly, &tcupTripwireBody{t}, "multipart/form-data; boundary=x", 0).Code; got != http.StatusForbidden {
		t.Errorf("tech_cards:read only: want 403, got %d", got)
	}

	// The right to write FILES is not the right to import a tech card: sections do not lend each
	// other permissions, and an import creates a card.
	otherSection := authsrv.PutAdminAuthz(context.Background(), authsrv.AdminAuthz{
		Perms: map[string]entity.AccessLevel{rbac.SectionFiles: entity.AccessWrite},
	})
	if got := tcupServe(t, s, otherSection, &tcupTripwireBody{t}, "multipart/form-data; boundary=x", 0).Code; got != http.StatusForbidden {
		t.Errorf("files:write only: want 403, got %d", got)
	}
}

// ────────────────────────────── 2. what is not an archive ──────────────────────────────

// TestTechcardArchiveUploadRefusesWhatIsNotAnArchive: rubbish in the part is the caller's problem
// (400), not ours (500) — and it leaves NOTHING behind: no row, and the object that was already
// written is removed, because nothing will ever point at it.
func TestTechcardArchiveUploadRefusesWhatIsNotAnArchive(t *testing.T) {
	s, cards, fs := tcupServer(t)
	tcupBucket(t, fs)
	row := tcupExpectRow(cards)

	var removed []string
	fs.EXPECT().RemoveObjectsByKeys(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, keys ...string) error {
			removed = append(removed, keys...)
			return nil
		}).Once()

	body, ct := tcupBody(t, "archive", []byte("this is not a zip, it is a sentence"))
	w := tcupServe(t, s, tcupWriterCtx(), body, ct, 0)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("garbage instead of a zip: want 400, got %d (%s)", w.Code, w.Body.String())
	}
	if msg := tcupErrorText(t, w); !strings.Contains(msg, "ZIP") {
		t.Errorf("the refusal must say what is wrong in words a person can act on, got %q", msg)
	}
	if *row != nil {
		t.Errorf("a refused archive must not be recorded, got %+v", *row)
	}
	if len(removed) != 1 || !strings.HasPrefix(removed[0], techcardarchive.BucketPrefixImports) {
		t.Errorf("the orphaned object must be removed, got %v", removed)
	}
}

// TestTechcardArchiveUploadRefusesTheWrongShapeOfRequest covers the two ways the request itself is
// malformed, both of which must be told apart from "your archive is broken".
func TestTechcardArchiveUploadRefusesTheWrongShapeOfRequest(t *testing.T) {
	s, cards, fs := tcupServer(t)
	tcupBucket(t, fs)
	row := tcupExpectRow(cards)

	// Not multipart at all.
	w := tcupServe(t, s, tcupWriterCtx(), bytes.NewReader([]byte("PK\x03\x04")), "application/zip", 0)
	if w.Code != http.StatusBadRequest {
		t.Errorf("a non-multipart body: want 400, got %d", w.Code)
	}

	// Multipart, but the part is not the archive.
	body, ct := tcupBody(t, "meta", []byte("{}"))
	w = tcupServe(t, s, tcupWriterCtx(), body, ct, 0)
	if w.Code != http.StatusBadRequest {
		t.Errorf("a wrongly named part: want 400, got %d", w.Code)
	}
	if msg := tcupErrorText(t, w); !strings.Contains(msg, "archive") {
		t.Errorf("the refusal must name the part it wanted, got %q", msg)
	}
	if *row != nil {
		t.Errorf("a malformed request must not be recorded, got %+v", *row)
	}
}

// ────────────────────────────── 3. the happy path ──────────────────────────────

// TestTechcardArchiveUploadRecordsTheRowAndAnswersWithTheReport is the whole route end to end: the
// bytes go into the bucket, come back out, are opened, resolved and reported on, and exactly ONE
// row is written — with the columns the commit will need.
func TestTechcardArchiveUploadRecordsTheRowAndAnswersWithTheReport(t *testing.T) {
	s, cards, fs := tcupServer(t)
	tcupBucket(t, fs)
	row := tcupExpectRow(cards)

	colorways := []byte(`[{"color_code":"BLK","base_sku":"GRB-SS26-014-BLK","recipe":[]}]`)
	manifest := tcupManifestJSON(`{"media": 0, "patterns": 0, "markers": 0, "materials": 0}`, "")
	body, ct := tcupBody(t, "archive", tcupArchive(t, manifest, map[string][]byte{
		techcardarchive.FileColorways: colorways,
	}))

	w := tcupServe(t, s, tcupWriterCtx(), body, ct, 0)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}

	importID, dryRun, rep := tcupDecode(t, w)
	if len(importID) != tcupImportIDLen {
		t.Errorf("import_id must be the CHAR(26) key the commit call names, got %q", importID)
	}
	if !dryRun {
		t.Error("the upload's report is a PREVIEW and has to say so: dry_run must be true")
	}
	if rep.GetImportId() != importID {
		t.Errorf("the report names import %q, the body names %q", rep.GetImportId(), importID)
	}
	if rep.GetStyleNumber() != "GRB-SS26-014" {
		t.Errorf("style_number: want the archive's, got %q", rep.GetStyleNumber())
	}
	if rep.GetStage() != string(entity.TechCardStageFit) {
		t.Errorf("stage: want the card's own %q, got %q", entity.TechCardStageFit, rep.GetStage())
	}
	// Every counted entity is present even at zero — a missing row and a zero row must never be the
	// same thing on the wire.
	if got := len(rep.GetCounters()); got < len(techcardarchive.CountedEntities) {
		t.Errorf("counters: want at least %d rows, got %d", len(techcardarchive.CountedEntities), got)
	}
	// The colourway travelled as reference and the report says so, out loud, in the preview — this
	// is the line that tells the operator the recipe did NOT come with the card.
	var sawColorway bool
	for _, l := range rep.GetLines() {
		if l.GetReason() == string(techcardarchive.ReasonColorwaysNotApplied) {
			sawColorway = true
		}
	}
	if !sawColorway {
		t.Error("a colourway in the archive must produce a colorways_not_applied line in the DRY RUN")
	}

	if *row == nil {
		t.Fatal("the upload must record exactly one tech_card_import row")
	}
	got := *row
	if got.importID != importID {
		t.Errorf("row import_id %q, response %q", got.importID, importID)
	}
	if got.objectKey != techcardarchive.BucketPrefixImports+importID+".zip" {
		t.Errorf("row object_key %q does not address the uploaded object", got.objectKey)
	}
	if got.importedBy != "im" {
		t.Errorf("imported_by: want the authenticated username, got %q", got.importedBy)
	}
	if !bytes.Equal(got.colorways, colorways) {
		t.Errorf("colorways_payload must be the archive's own bytes\n want: %s\n  got: %s", colorways, got.colorways)
	}
	if !bytes.Equal(got.manifest, manifest) {
		t.Errorf("archive_manifest must be manifest.json verbatim\n want: %s\n  got: %s", manifest, got.manifest)
	}
}

// TestTechcardArchiveUploadWithoutColorwaysStoresNoPayload: NULL is the honest value for "no
// colourways travelled", and an empty JSON array is not the same statement.
func TestTechcardArchiveUploadWithoutColorwaysStoresNoPayload(t *testing.T) {
	s, cards, fs := tcupServer(t)
	tcupBucket(t, fs)
	row := tcupExpectRow(cards)

	body, ct := tcupBody(t, "archive", tcupArchive(t, tcupManifestJSON("", ""), nil))
	if w := tcupServe(t, s, tcupWriterCtx(), body, ct, 0); w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if *row == nil {
		t.Fatal("the upload must record one row")
	}
	if len((*row).colorways) != 0 {
		t.Errorf("no colorways.json in the archive must store no payload, got %s", (*row).colorways)
	}
}

// ────────────────────────────── 4. the manifest is BYTES ──────────────────────────────

// TestTechcardArchiveUploadStoresTheManifestVerbatim is the guard against the quietest defect this
// route can have.
//
// A 1.x archive carries manifest fields this server's struct has no member for. json.Unmarshal
// drops them without a word, so a handler that stored a RE-MARSHAL of the parsed manifest would
// write a shorter manifest than the one that arrived — under a column whose comment reads "что было
// в ZIP на загрузке". Nothing would ever fail; the journal would simply be wrong forever.
//
// The test carries its own positive control: it first proves that a round trip DOES lose the field
// (so a green result cannot come from the field surviving by accident), and only then demands that
// the stored bytes are byte-identical to the entry in the ZIP.
func TestTechcardArchiveUploadStoresTheManifestVerbatim(t *testing.T) {
	s, cards, fs := tcupServer(t)
	tcupBucket(t, fs)
	row := tcupExpectRow(cards)

	// Two shapes of "newer MINOR": a whole top-level field, and one nested inside a struct this
	// server DOES know — the second is the one a `json:"-"`-free struct silently eats.
	const newerMinorField = `"factory_notes"`
	manifest := tcupManifestJSON("", `"factory_notes": {"cut_by": "Lanificio", "sheet": 7}, "id_maps_extra": ["future"]`)

	// POSITIVE CONTROL: the parse-then-re-marshal path really does destroy it. If this ever stops
	// being true, the assertion below stops proving anything and has to be rewritten, not deleted.
	var parsed techcardarchive.Manifest
	require.NoError(t, json.Unmarshal(manifest, &parsed))
	roundTripped, err := json.Marshal(parsed)
	require.NoError(t, err)
	if bytes.Contains(roundTripped, []byte(newerMinorField)) {
		t.Fatalf("this test cannot detect a re-marshal: the round trip kept %s", newerMinorField)
	}

	body, ct := tcupBody(t, "archive", tcupArchive(t, manifest, nil))
	if w := tcupServe(t, s, tcupWriterCtx(), body, ct, 0); w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if *row == nil {
		t.Fatal("the upload must record one row")
	}
	stored := (*row).manifest
	if !bytes.Contains(stored, []byte(newerMinorField)) {
		t.Errorf("the stored manifest lost the newer MINOR's field — it was re-marshalled, not kept:\n%s", stored)
	}
	if !bytes.Equal(stored, manifest) {
		t.Errorf("archive_manifest must be the ZIP entry byte for byte\n want: %s\n  got: %s", manifest, stored)
	}
}

// ────────────────────────────── 5. the ceiling ──────────────────────────────

// TestTechcardArchiveUploadRefusesABodyOverTheCeiling: over the ceiling is 413 and NOT 500 — the
// difference is "send a smaller file" versus "the server is broken, try again", and the second
// sends the operator hunting for a fault that does not exist. Nor is it a truncation: a silently
// cut ZIP is indistinguishable from a corrupt one, so nothing is recorded either.
//
// The cap here is 512 bytes rather than 256 MiB for the obvious reason; what the test exercises is
// the CLASSIFICATION of the error MaxBytesReader produces, which is the same error at either size.
func TestTechcardArchiveUploadRefusesABodyOverTheCeiling(t *testing.T) {
	s, cards, fs := tcupServer(t)
	tcupBucket(t, fs)
	row := tcupExpectRow(cards)

	body, ct := tcupBody(t, "archive", tcupArchive(t, tcupManifestJSON("", ""), nil))
	w := tcupServe(t, s, tcupWriterCtx(), body, ct, 512)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body over the ceiling: want 413, got %d (%s)", w.Code, w.Body.String())
	}
	if msg := tcupErrorText(t, w); !strings.Contains(msg, "larger than") {
		t.Errorf("the refusal must say the archive is too big, got %q", msg)
	}
	if *row != nil {
		t.Errorf("a body over the ceiling must record nothing, got %+v", *row)
	}
}

// ────────────────────────────── 6. the positive control ──────────────────────────────

// TestTechcardArchiveUploadFailsThePositiveControl: the manifest claims media and patterns, the
// parse produced none of either, and that is a DEAD PARSER rather than a clean card. It must not
// come back as 200 with a reassuring empty report — which is exactly what it would look like
// without this check, and exactly the screen an operator would believe.
func TestTechcardArchiveUploadFailsThePositiveControl(t *testing.T) {
	s, cards, fs := tcupServer(t)
	tcupBucket(t, fs)
	row := tcupExpectRow(cards)

	var removed []string
	fs.EXPECT().RemoveObjectsByKeys(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, keys ...string) error {
			removed = append(removed, keys...)
			return nil
		}).Once()

	// contents says 14 media and 6 patterns; the archive carries no index for either, so every
	// counter for them stays at zero.
	manifest := tcupManifestJSON(`{"media": 14, "patterns": 6, "markers": 0, "materials": 0}`, "")
	body, ct := tcupBody(t, "archive", tcupArchive(t, manifest, nil))

	w := tcupServe(t, s, tcupWriterCtx(), body, ct, 0)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a report that failed its positive control: want 400, got %d (%s)", w.Code, w.Body.String())
	}
	if msg := tcupErrorText(t, w); !strings.Contains(msg, "did not parse") {
		t.Errorf("the refusal must say the archive did not parse, got %q", msg)
	}
	if *row != nil {
		t.Errorf("a failed positive control must record nothing, got %+v", *row)
	}
	if len(removed) != 1 {
		t.Errorf("the orphaned object must be removed, got %v", removed)
	}
}
