package store

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/jpk"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф3.6 — THE ADVERSARIAL GATE: AN IMPORTED CARD MAY NEVER LOOK SIGNED.
//
// This is not a cosmetic assertion about a badge. The create pipeline does not REFUSE sign-offs it
// is handed — prepareCreateTechCardSignoffs stamps them with the acting username and the current
// time, and restampFreshSignoffDigests then fingerprints the payload being written. So an APPROVED
// sign-off that arrives in a file comes back out as a FRESH, internally consistent, digest-matching
// approval attributed to whoever ran the import. There is no later gate that can undo that, and
// «the archive brought signatures, refuse it» would be the wrong shape of defence anyway: by the
// time anything downstream could look, the signatures would already be legitimate-looking. The only
// defence is to never put them on the input, and this test is what stands over that.
//
// WHAT IT MEASURES, and it walks the LIVE pipeline to do it — real MySQL, the real export RPC, the
// real multipart import route, the real commit RPC:
//
//	source card → 7 approved sign-offs → released (+ release snapshot) → export → import
//	                                                                              ↓
//	  new card: approval_state=draft, released_at NULL, approved_at NULL,
//	            0 rows in tech_card_signoff, 0 rows in tech_card_release,
//	            and one journal line that says it came from an archive.
//
// TWO ARCHIVES, NOT ONE, and the second is the one that matters. Our own exporter already cuts
// sign-offs out of card.json (sanitizeCardForArchive), so a round trip of OUR file proves only that
// our exporter is polite — the sign-off assertions on it are true by an accident of the source, not
// by the import's doing. The sanitiser exists for the archive our exporter did NOT write
// (sanitize.go says so in its own header: «hand-made, produced by a future MINOR, or hostile»), so
// the second case takes the very archive we just produced, writes the seven APPROVED sign-offs and
// the RELEASED stamp back into card.json, re-zips it and imports THAT. Everything downstream is
// production code meeting a file it did not make, which is the only shape in which this gate has
// anything to guard.
//
// POSITIVE CONTROLS, because every assertion here is «something is absent» and an absence proves
// nothing about a pipeline that produced nothing at all:
//   - the SOURCE card is re-read after the export and must still be released, still hold its seven
//     approvals and its release row — a fixture that silently failed to sign would make every
//     assertion below vacuously true;
//   - the archive's manifest must say approval_state_at_export=released and its card.json must
//     carry approval_state RELEASED — i.e. the sanitiser is handed something to remove. If the
//     export ever starts stripping the state as well, this line turns red and says so, instead of
//     letting the gate quietly become a sentinel over an empty input;
//   - the hostile archive is re-opened and re-parsed before it is uploaded, and its card.json must
//     read back with seven APPROVED sign-offs — a rewrite that silently produced nothing would
//     otherwise be indistinguishable from a working defence.
//
// WHICH DEFENCE THIS TEST ACTUALLY WATCHES — MEASURED (2026-08-26), because a gate nobody measured
// is a gate nobody knows is connected. Three defences stand between an archive and a signed-looking
// card (import.go's header names them): (1) the export writes no sign-offs into the file, (2) the
// resolver's SanitizeImportedCard, (3) the store's prepareImportedCard. Mutations run one at a time
// against this test, on a real container:
//
//	(2) alone disabled ..................... 3 PASS / 0 FAIL — MASKED by (3), which repeats it.
//	(2) and (3) both disabled .............. 0 PASS / 3 FAIL on approval_state ("released").
//	only the SIGN-OFF half of (2)+(3) ...... 1 PASS / 2 FAIL — red on the HOSTILE case only,
//	                                         7 sign-off rows on the imported card; the polite
//	                                         round trip stayed green because (1) had already cut
//	                                         them out of our own archive.
//
// Read the third line before deleting the hostile case: without it the sign-off assertions of this
// test are true before the import runs, and the half of the gate that guards the actual danger —
// a create pipeline that COERCES supplied signatures into fresh ones — would be a sentinel over
// dead code. Read the first line before believing that disabling the sanitiser alone is a red test:
// it is not, and any report claiming otherwise did not run it.
//
// SAFE ONLY against a local container DSN. This suite's TestMain drops every table on cleanup
// (mysql_test.go, project memory store-tests-drop-prod-db), and that is a property of the PACKAGE,
// not of this test: the guard below cannot save a database that `go test ./internal/store/` was
// already pointed at. It is here for the next person who types the command from memory — it says
// the word «container» instead of failing with a wall of missing-table errors.
// ─────────────────────────────────────────────────────────────────────────────

func TestTechCardArchiveSignatureGate(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

	// Named in the log so a run of this test is EVIDENCE of what it ran against. Credentials are cut
	// — the host, the port and the schema are the whole question — and a run whose line does not say
	// a localhost port is a run whose result means nothing.
	t.Logf("integration target: %s", sigGateDSNTarget(testCfg.DSN))

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	{
		di, derr := s.Cache().GetDictionaryInfo(ctx)
		require.NoError(t, derr)
		hf, herr := s.Hero().GetHero(ctx)
		require.NoError(t, herr)
		require.NoError(t, cache.InitConsts(ctx, di, hf))
	}

	const actor = "sig-gate-operator"
	// The handlers read the acting username and the section rights out of context; outside the gRPC
	// path nothing puts them there, and the costing/RBAC checks fail CLOSED on a bare context.
	ctx = authsrv.PutAdminUsername(ctx, actor)
	ctx = authsrv.PutAdminAuthz(ctx, authsrv.AdminAuthz{
		Perms: map[string]entity.AccessLevel{rbac.SectionTechCards: entity.AccessWrite},
	})

	bucket := newSigGateBucket()
	srv, err := admin.New(s, bucket, nil, nil, nil, nil, nil, nil, nil, nil,
		entity.LabelAddress{}, "", "", nil, jpk.Taxpayer{}, decimal.Zero)
	require.NoError(t, err)

	// ── the fixture, in the ONE order a released card can be built in ──────────────────────────
	// A released card is FROZEN for edits (techcard.go's freeze check), so the content and the
	// signatures have to be on it before the release transition, never after.
	srcID := sigGateSeedSignedReleasedCard(ctx, t, s, actor)

	// ── positive control on the fixture itself ────────────────────────────────────────────────
	sigGateRequireSourceIsSigned(ctx, t, srcID)

	// ── export ────────────────────────────────────────────────────────────────────────────────
	exp, err := srv.ExportTechCardArchive(ctx, &pb_admin.ExportTechCardArchiveRequest{
		TechCardId: int32(srcID),
	})
	require.NoError(t, err, "the export of a released card must succeed")
	require.NotEmpty(t, exp.GetUrl(), "an export with no link is not an export")
	require.Equal(t, string(entity.TechCardApprovalReleased),
		exp.GetManifest().GetSource().GetApprovalStateAtExport(),
		"the manifest must record that a RELEASED card is what travelled — otherwise this test "+
			"is exporting a draft and proving nothing")

	archiveBytes := bucket.lastArchive(t)

	// The export is unchanged by the fixture: the SOURCE must still be signed and released after it.
	sigGateRequireSourceIsSigned(ctx, t, srcID)

	// ── the sanitiser is handed something to remove (positive control on the input) ────────────
	polite, err := techcardarchive.OpenArchive(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	require.NoError(t, err, "our own archive must open with our own reader")
	politeCard, err := polite.CardJSON()
	require.NoError(t, err)
	require.Equal(t, pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_RELEASED,
		politeCard.GetTechCard().GetApprovalState(),
		"card.json carries the source's approval state — if it stops doing so, the import-side "+
			"sanitiser has nothing left to strip on this path and this gate must be re-argued")
	require.NotNil(t, politeCard.GetTechCard().GetReleasedAt(),
		"card.json carries the source's release stamp")
	// Defence (1): our exporter cuts sign-offs out of the file, which is exactly why the hostile
	// case below exists — on THIS archive the sign-off assertions are true before the import runs.
	require.Empty(t, politeCard.GetTechCard().GetSignoffs(),
		"our own exporter must not put sign-offs in the archive at all")

	t.Run("our own export", func(t *testing.T) {
		newID := sigGateImport(ctx, t, srv, bucket, archiveBytes)
		t.Cleanup(func() { _ = s.TechCards().DeleteTechCard(context.Background(), newID) })
		require.NotEqual(t, srcID, newID, "an import always creates; it never overwrites")
		sigGateRequireNotSigned(ctx, t, newID)
		sigGateRequireImportJournalled(ctx, t, newID)
		// And the source is untouched by somebody importing a copy of it.
		sigGateRequireSourceIsSigned(ctx, t, srcID)
	})

	t.Run("hostile archive", func(t *testing.T) {
		hostile := sigGateForgeSignedArchive(t, archiveBytes, actor)

		// Positive control on the forgery: an archive that quietly failed to carry the signatures
		// would make this whole case a second copy of the polite one.
		forged, err := techcardarchive.OpenArchive(bytes.NewReader(hostile), int64(len(hostile)))
		require.NoError(t, err, "the forged archive must still be a legal archive — otherwise it is "+
			"refused for being malformed and the sanitiser is never reached")
		forgedCard, err := forged.CardJSON()
		require.NoError(t, err)
		require.Len(t, forgedCard.GetTechCard().GetSignoffs(), len(sigGateSections),
			"the forgery must actually carry the approvals it claims to")
		for _, so := range forgedCard.GetTechCard().GetSignoffs() {
			require.Equal(t, pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED, so.GetState())
		}
		require.Equal(t, pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_RELEASED,
			forgedCard.GetTechCard().GetApprovalState())
		require.NotNil(t, forgedCard.GetTechCard().GetReleasedAt())

		newID := sigGateImport(ctx, t, srv, bucket, hostile)
		t.Cleanup(func() { _ = s.TechCards().DeleteTechCard(context.Background(), newID) })
		sigGateRequireNotSigned(ctx, t, newID)
		sigGateRequireImportJournalled(ctx, t, newID)
	})
}

// sigGateDSNTarget is the tcp(host:port)/schema half of a DSN — everything before the '@' is the
// credential and is never printed.
func sigGateDSNTarget(dsn string) string {
	if at := strings.LastIndex(dsn, "@"); at >= 0 {
		dsn = dsn[at+1:]
	}
	if q := strings.Index(dsn, "?"); q >= 0 {
		dsn = dsn[:q]
	}
	return dsn
}

// sigGateSections is the seven the card is signed on — the whole of ValidTechCardSignoffSections,
// spelled out rather than ranged over a map so the count in the assertions is a number a reader can
// check against FORMAT.md and the order is stable.
var sigGateSections = []entity.TechCardSignoffSection{
	entity.SignoffDesign, entity.SignoffConstruction, entity.SignoffMaterials,
	entity.SignoffColour, entity.SignoffLabels, entity.SignoffPackaging, entity.SignoffCosting,
}

// sigGatePbSections is the same seven in the wire enum, for the forged card.json.
var sigGatePbSections = []pb_common.TechCardSignoffSection{
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_DESIGN,
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_CONSTRUCTION,
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_MATERIALS,
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_COLOUR,
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_LABELS,
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_PACKAGING,
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_COSTING,
}

// sigGateSeedSignedReleasedCard builds the source: content first, then the seven approvals, then
// the release transition, then the release snapshot.
//
// The snapshot is written by calling SaveTechCardRelease directly rather than through the RPC,
// because the live path writes it BEST-EFFORT AFTER the transaction (snapshotReleaseIfReleased) and
// a test that raced that would be testing the scheduler. The sequence reproduced here is the
// handler's own — consistent reload → ConvertEntityTechCardToPb → protojson → SaveTechCardRelease —
// so what lands in tech_card_release is the row the live path lands.
func sigGateSeedSignedReleasedCard(ctx context.Context, t *testing.T, s *MYSQLStore, actor string) int {
	t.Helper()

	tag := fmt.Sprintf("SG-%d", time.Now().UnixNano()%1_000_000_000)
	signedAt := sql.NullTime{Time: time.Now().UTC().Add(-time.Hour).Truncate(time.Second), Valid: true}
	signoffs := make([]entity.TechCardSignoff, 0, len(sigGateSections))
	for _, sec := range sigGateSections {
		signoffs = append(signoffs, entity.TechCardSignoff{
			Section:      sec,
			State:        entity.SignoffStateApproved,
			SignedBy:     sql.NullString{String: "the source instance's technologist", Valid: true},
			SignedAt:     signedAt,
			Note:         sql.NullString{String: "approved on the source instance", Valid: true},
			SignedDigest: sql.NullString{String: strings.Repeat("a", 64), Valid: true},
		})
	}

	card := &entity.TechCardInsert{
		StyleNumber:     sql.NullString{String: tag, Valid: true},
		Name:            tag + " signed jacket",
		Stage:           entity.TechCardStageProto,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		CreatedBy:       actor,
		UpdatedBy:       actor,
		BomItems: []entity.TechCardBomItem{
			{LineKey: "SGBOMSHELL0000000000000001", Section: entity.BomSectionFabric, Name: "shell fabric"},
			{LineKey: "SGBOMTHREAD000000000000001", Section: entity.BomSectionTrim, Name: "thread"},
		},
		Signoffs: signoffs,
	}
	srcID, err := s.TechCards().AddTechCard(ctx, card)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.TechCards().DeleteTechCard(context.Background(), srcID) })

	// RELEASE through the regular save, carrying the sign-offs back as read: this is the transition
	// that freezes the card, and after it nothing may edit it.
	stored, err := s.TechCards().GetTechCardById(ctx, srcID)
	require.NoError(t, err)
	require.Len(t, stored.Signoffs, len(sigGateSections),
		"the seven approvals must be on the card BEFORE it is released — a released card is frozen")
	rel := stored.TechCardInsert
	rel.ApprovalState = entity.TechCardApprovalReleased
	rel.UpdatedBy = actor
	require.NoError(t, s.TechCards().UpdateTechCard(ctx, srcID, &rel, stored.LockVersion))

	// The release snapshot, the handler's own sequence.
	released, err := s.TechCards().GetTechCardByIdConsistent(ctx, srcID)
	require.NoError(t, err)
	rates, err := s.TechCards().GetCostingFxRatesToBase(ctx)
	require.NoError(t, err)
	fx := dto.CostingFx{ToBase: rates, Base: cache.GetBaseCurrency()}
	blob, err := protojson.Marshal(dto.ConvertEntityTechCardToPb(released, fx))
	require.NoError(t, err)
	require.NoError(t, s.TechCards().SaveTechCardRelease(ctx, entity.TechCardRelease{
		TechCardReleaseMeta: entity.TechCardReleaseMeta{
			TechCardId: srcID,
			ReleasedBy: sql.NullString{String: actor, Valid: true},
			Currency:   sql.NullString{String: cache.GetBaseCurrency(), Valid: true},
		},
		Snapshot: string(blob),
	}))
	return srcID
}

// sigGateRequireSourceIsSigned is the positive control: the fixture really is a signed, released
// card with a release row. Run before AND after the export, so «the export quietly unsigned it» is
// not a way for the assertions on the new card to come out true.
func sigGateRequireSourceIsSigned(ctx context.Context, t *testing.T, techCardID int) {
	t.Helper()

	var state string
	var releasedAt, approvedAt sql.NullTime
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT approval_state, released_at, approved_at FROM tech_card WHERE id = ?", techCardID).
		Scan(&state, &releasedAt, &approvedAt))
	require.Equal(t, string(entity.TechCardApprovalReleased), state, "the source must be RELEASED")
	require.True(t, releasedAt.Valid, "a released source must carry a release stamp")
	require.True(t, approvedAt.Valid, "a released source must carry an approval stamp")

	var approved int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tech_card_signoff WHERE tech_card_id = ? AND state = 'approved'",
		techCardID).Scan(&approved))
	require.Equal(t, len(sigGateSections), approved, "the source must carry all seven approvals")

	var releases int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tech_card_release WHERE tech_card_id = ?", techCardID).Scan(&releases))
	require.Equal(t, 1, releases, "the source must carry its release snapshot")
}

// sigGateRequireNotSigned is the gate itself, read off the COLUMNS rather than off any read model:
// what a person sees on the card is what these rows say, and a read-side projection that happened
// to hide a signature would be a different bug from the one this test is about.
func sigGateRequireNotSigned(ctx context.Context, t *testing.T, techCardID int) {
	t.Helper()

	var state string
	var releasedAt, approvedAt sql.NullTime
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT approval_state, released_at, approved_at FROM tech_card WHERE id = ?", techCardID).
		Scan(&state, &releasedAt, &approvedAt))
	require.Equal(t, string(entity.TechCardApprovalDraft), state,
		"an imported card must arrive a DRAFT — «released» here is an assertion nobody in this "+
			"building made")
	require.False(t, releasedAt.Valid,
		"released_at must be empty: a release stamp is a claim this instance released the card")
	require.False(t, approvedAt.Valid,
		"approved_at must be empty for the same reason released_at must")

	var signoffs int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tech_card_signoff WHERE tech_card_id = ?", techCardID).Scan(&signoffs))
	require.Zero(t, signoffs,
		"an imported card must carry NO sign-off rows at all — the create pipeline re-stamps the "+
			"ones it is handed with the importing operator's name, so any row here is a signature "+
			"minted out of a file")

	var releases int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tech_card_release WHERE tech_card_id = ?", techCardID).Scan(&releases))
	require.Zero(t, releases, "an imported card has no release history — it was never released here")
}

// sigGateRequireImportJournalled checks the other half of FORMAT.md §6.5: the card's permanent
// history says where it came from, and it says it in a line THIS instance wrote.
func sigGateRequireImportJournalled(ctx context.Context, t *testing.T, techCardID int) {
	t.Helper()

	rows, err := testDB.QueryContext(ctx,
		"SELECT action, section, COALESCE(change_note, '') FROM tech_card_revision WHERE tech_card_id = ?",
		techCardID)
	require.NoError(t, err)
	defer rows.Close()

	var notes []string
	var imported bool
	for rows.Next() {
		var action, section, note string
		require.NoError(t, rows.Scan(&action, &section, &note))
		notes = append(notes, fmt.Sprintf("%s/%s: %s", section, action, note))
		if strings.Contains(strings.ToLower(note), "imported from archive") {
			imported = true
		}
	}
	require.NoError(t, rows.Err())
	require.True(t, imported,
		"the journal must carry the line saying the card came from an archive; it holds: %v", notes)
	// §6.5 — exactly one entry, and this instance wrote it: the archive's own revision journal is
	// never appended to the target's, so no line of this card's permanent history is a statement
	// another base made.
	require.Len(t, notes, 1,
		"an imported card's journal holds exactly ONE entry — the one this instance wrote; it holds: %v", notes)
}

// sigGateImport pushes one archive through the REAL import door — the multipart route, then the
// commit RPC — and returns the id of the card it made.
func sigGateImport(ctx context.Context, t *testing.T, srv *admin.Server,
	bucket *sigGateBucket, archive []byte) int {
	t.Helper()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("archive", "techcard.zip")
	require.NoError(t, err)
	_, err = part.Write(archive)
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	req := httptest.NewRequest(http.MethodPost, "/api/techcard-archive/upload", &body).WithContext(ctx)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.TechCardArchiveUploadHandler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "the upload route answered: %s", rec.Body.String())

	var up struct {
		ImportID string          `json:"import_id"`
		DryRun   bool            `json:"dry_run"`
		Report   json.RawMessage `json:"report"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &up))
	require.NotEmpty(t, up.ImportID)
	require.True(t, up.DryRun, "the upload route writes nothing; it answers with a dry run")

	// The dry run wrote no card: the gate below must be measuring what the COMMIT did.
	var cardsForImport sql.NullInt32
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT tech_card_id FROM tech_card_import WHERE import_id = ?", up.ImportID).Scan(&cardsForImport))
	require.False(t, cardsForImport.Valid, "a dry run must not have created a card")

	res, err := srv.CommitTechCardImport(ctx, &pb_admin.CommitTechCardImportRequest{ImportId: up.ImportID})
	require.NoError(t, err, "the commit of a legal archive must succeed")
	require.NotZero(t, res.GetTechCardId())
	return int(res.GetTechCardId())
}

// sigGateForgeSignedArchive rewrites card.json of a legal archive so that the card inside it arrives
// APPROVED, RELEASED and signed on all seven sections — the hand-made archive sanitize.go says it
// exists for.
//
// It rebuilds the ZIP rather than patching it in place because a ZIP directory carries each entry's
// uncompressed length and CRC, and the reader checks the body against the declared length. card.json
// itself carries no sha in the manifest (only media / patterns / markers do), so nothing else has to
// be recomputed — which is precisely why an attacker would use this door.
func sigGateForgeSignedArchive(t *testing.T, archive []byte, actor string) []byte {
	t.Helper()

	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	require.NoError(t, err)

	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	seenCard := false
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		raw, err := io.ReadAll(rc)
		require.NoError(t, rc.Close())
		require.NoError(t, err)

		if f.Name == techcardarchive.FileCard {
			seenCard = true
			raw = sigGateSignCardJSON(t, raw, actor)
		}
		w, err := zw.Create(f.Name)
		require.NoError(t, err)
		_, err = w.Write(raw)
		require.NoError(t, err)
	}
	require.True(t, seenCard, "the archive must contain %s", techcardarchive.FileCard)
	require.NoError(t, zw.Close())
	return out.Bytes()
}

// sigGateSignCardJSON is the forgery proper: parse card.json the way the import parses it, put the
// approval family back on, and re-marshal.
func sigGateSignCardJSON(t *testing.T, raw []byte, actor string) []byte {
	t.Helper()

	var card pb_common.TechCard
	require.NoError(t, (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, &card))
	ins := card.GetTechCard()
	require.NotNil(t, ins, "card.json must carry the writable half")

	signedAt := timestamppb.New(time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second))
	ins.ApprovalState = pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_RELEASED
	ins.ReleasedAt = signedAt
	ins.ApprovedAt = signedAt
	ins.Signoffs = nil
	for _, sec := range sigGatePbSections {
		ins.Signoffs = append(ins.Signoffs, &pb_common.TechCardSignoff{
			Section:      sec,
			State:        pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED,
			SignedBy:     "a name from somebody else's admin table",
			SignedAt:     signedAt,
			Note:         "approved elsewhere, on evidence this instance has never seen",
			SignedDigest: strings.Repeat("f", 64),
		})
	}
	// Nothing here pretends to be the importing operator; that is the point. If the gate ever
	// leaked, the create pipeline would re-stamp these with `actor` and make them look legitimate.
	_ = actor

	forged, err := protojson.Marshal(&card)
	require.NoError(t, err)
	return forged
}

// ────────────────────────────── the bucket this test needs ──────────────────────────────

// sigGateBucket is the smallest FileStore an export→import round trip can run on: objects in a map.
//
// dependency.FileStore is EMBEDDED AND LEFT NIL on purpose. Every method this round trip does not
// use therefore panics with a nil-pointer rather than returning a plausible zero value — a fake
// that answers «no error, nothing there» to a call nobody expected is how a test goes green over a
// path it never exercised. The card in this fixture carries no media and no patterns, so the only
// bucket traffic is the archive object and the import object; anything else reaching this type is a
// change in the pipeline that has to be looked at, and the panic names the method.
type sigGateBucket struct {
	dependency.FileStore

	mu      sync.Mutex
	objects map[string][]byte
	// archives is the keys of the archive objects in the order they were written, so a test can ask
	// for «the one the export just made» without guessing the name.
	archives []string
}

func newSigGateBucket() *sigGateBucket {
	return &sigGateBucket{objects: map[string][]byte{}}
}

func (b *sigGateBucket) UploadArchiveObject(_ context.Context, r io.Reader, name string) (string, error) {
	// Read to EOF and keep what arrived, exactly as a streaming consumer must: the export writes
	// into an io.Pipe, and a fake that did not drain it would deadlock the writer instead of
	// failing. io.ReadAll surfacing the pipe's error is what makes a broken writer red here.
	body, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	key := techcardarchive.BucketPrefixArchives + fmt.Sprintf("%d/", len(b.archives)) + name
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects[key] = body
	b.archives = append(b.archives, key)
	return key, nil
}

func (b *sigGateBucket) PresignArchiveObject(_ context.Context, objectKey string, ttl time.Duration) (
	string, time.Time, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.objects[objectKey]; !ok {
		return "", time.Time{}, fmt.Errorf("presign %s: no such object", objectKey)
	}
	return "https://sig-gate.invalid/" + objectKey, time.Now().Add(ttl), nil
}

func (b *sigGateBucket) UploadImportObject(_ context.Context, r io.Reader, importID string) (string, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	key := techcardarchive.BucketPrefixImports + importID + ".zip"
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects[key] = body
	return key, nil
}

func (b *sigGateBucket) GetImportObjectReaderAt(_ context.Context, objectKey string) (
	dependency.ReaderAtCloser, int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	body, ok := b.objects[objectKey]
	if !ok {
		return nil, 0, fmt.Errorf("read %s: no such object", objectKey)
	}
	return sigGateReaderAt{bytes.NewReader(body)}, int64(len(body)), nil
}

func (b *sigGateBucket) RemoveObjectsByKeys(_ context.Context, keys ...string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, k := range keys {
		delete(b.objects, k)
	}
	return nil
}

func (b *sigGateBucket) DeleteObjects(_ context.Context, _ ...string) error { return nil }

// lastArchive is the bytes of the most recent export.
func (b *sigGateBucket) lastArchive(t *testing.T) []byte {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	require.NotEmpty(t, b.archives, "the export must have written an archive object")
	return b.objects[b.archives[len(b.archives)-1]]
}

// sigGateReaderAt is a *bytes.Reader with the Close the import path defers.
type sigGateReaderAt struct{ *bytes.Reader }

func (sigGateReaderAt) Close() error { return nil }
