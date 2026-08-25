package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/jekabolt/grbpwr-manager/internal/store/techcard"
	"github.com/jekabolt/grbpwr-manager/internal/techcardanalysis"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

// --- the stand -----------------------------------------------------------------------------------

// tcaCard is a small card that says something to the machine layer: two machine steps that assemble
// a unit, a piece, and ONE BOM line priced in a foreign currency.
//
// The PLN line is the load-bearing part of this fixture. It is what makes a finding depend on the
// FX rates — the one piece of plumbing in this handler that a green test can most easily fake, since
// the analyzer would happily run with an empty rate map and never say so.
func tcaCard() *entity.TechCard {
	ns := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	ni := func(n int32) sql.NullInt32 { return sql.NullInt32{Int32: n, Valid: true} }
	ndd := func(s string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
	}
	card := &entity.TechCard{Id: 7}
	card.ApprovalState = entity.TechCardApprovalInReview
	card.SizeIds = []int{3, 4}
	card.Pieces = []entity.TechCardPiece{{Id: 1, LineKey: "P1", Name: "перед"}}
	card.BomItems = []entity.TechCardBomItem{{
		Id: 1, LineKey: "B1", Name: "Карманка", Section: entity.BomSectionFabric,
		Unit:      ns("m"),
		UnitPrice: ndd("60"), Currency: ns("PLN"),
	}}
	card.Operations = []entity.TechCardOperation{
		{
			OperationNumber: ni(10),
			OperationType:   entity.OpTypeMachine,
			Zone:            entity.ZoneOuter,
			MachineType:     ns("lockstitch"),
			OutputUnitKey:   ns("front"),
			OutputUnitName:  ns("front"),
			AssemblyInputs:  []entity.OperationInput{{Key: "P1", Kind: entity.AssemblyInputPiece}},
			// PieceLineKeys mirrors the piece half of AssemblyInputs, which is what the read path
			// actually produces (it projects the legacy list out of the assembly table). Leaving it
			// empty would make this stand trip the legacy-divergence check on every run — a finding
			// that cannot occur on a card that came out of GetTechCardById.
			PieceLineKeys: []string{"P1"},
		},
		{
			OperationNumber: ni(20),
			OperationType:   entity.OpTypeMachine,
			Zone:            entity.ZoneOuter,
			MachineType:     ns("lockstitch"),
			OutputUnitKey:   ns("front"),
			OutputUnitName:  ns("front"),
			AssemblyInputs:  []entity.OperationInput{{Key: "front", Kind: entity.AssemblyInputUnit}},
		},
	}
	return card
}

// tcaStand wires a Server over mocks: one card read, one rate read. The rate expectation is NOT
// optional — an unstubbed GetCostingFxRatesToBase is exactly what makes the eleven baseline tests of
// this package red, and inheriting that debt in a brand new test would hide the FX wiring behind a
// mockery panic rather than exercise it.
func tcaStand(t *testing.T, card *entity.TechCard, cardErr error, rates map[string]decimal.Decimal) *Server {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	if cardErr != nil {
		tc.EXPECT().GetTechCardById(mock.Anything, mock.Anything).Return(nil, cardErr)
	} else {
		tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(card, nil)
	}
	// .Maybe() permits zero calls AND permits calls — it asserts nothing in either direction. The
	// claim that the gate refuses before touching the rate table is made by tcaStandThatForbidsFx
	// below, which registers no expectation at all.
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(rates, nil).Maybe()
	return &Server{repo: repo}
}

// tcaStandThatForbidsFx is tcaStand minus the rate expectation. mockery fails an unexpected call, so
// ANY read of the rate table on this stand fails the test by name — which is what makes "the gate
// refuses an oversized card before any rate is needed" an assertion rather than a comment.
func tcaStandThatForbidsFx(t *testing.T, card *entity.TechCard) *Server {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(card, nil)
	return &Server{repo: repo}
}

// tcaTitles projects the response down to what a reader would see in the list.
func tcaTitles(resp *pb_admin.GetTechCardConstructionAuditResponse) []string {
	out := make([]string, 0, len(resp.GetFindings()))
	for _, f := range resp.GetFindings() {
		out = append(out, fmt.Sprintf("[%s/%s] %s", f.GetSeverity(), f.GetCategory(), f.GetTitle()))
	}
	return out
}

// tcaHasTitleContaining reports whether any finding's title carries the substring.
func tcaHasTitleContaining(resp *pb_admin.GetTechCardConstructionAuditResponse, sub string) bool {
	for _, f := range resp.GetFindings() {
		if strings.Contains(f.GetTitle(), sub) {
			return true
		}
	}
	return false
}

// tcaCostingCtx is a SCOPED account that may see money: tech_cards:read to reach the RPC,
// costing:read to see the findings that quote prices. Scoped rather than super on purpose — super
// passes everything and would prove nothing about the predicate.
func tcaCostingCtx() context.Context {
	return authsrv.PutAdminAuthz(context.Background(), authsrv.AdminAuthz{
		Perms: map[string]entity.AccessLevel{
			rbac.SectionTechCards: entity.AccessRead,
			rbac.SectionCosting:   entity.AccessRead,
		},
	})
}

// tcaNoCostingCtx is the account the redaction exists for: it may read tech cards, and it may not
// see money. This is the real content-manager role named in the rbac.go rationale, not a synthetic
// edge case.
func tcaNoCostingCtx() context.Context {
	return authsrv.PutAdminAuthz(context.Background(), authsrv.AdminAuthz{
		Perms: map[string]entity.AccessLevel{rbac.SectionTechCards: entity.AccessRead},
	})
}

// --- happy path ----------------------------------------------------------------------------------

// TestGetTechCardConstructionAuditHappyPath: a real card comes back as findings + a fingerprint per
// numbered operation + the not-checked list, all of it converted onto the wire.
func TestGetTechCardConstructionAuditHappyPath(t *testing.T) {
	s := tcaStand(t, tcaCard(), nil, map[string]decimal.Decimal{})
	resp, err := s.GetTechCardConstructionAudit(tcaCostingCtx(),
		&pb_admin.GetTechCardConstructionAuditRequest{TechCardId: 7})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetFindings(), "a card with two steps and a foreign-priced BOM line must produce findings")

	// Every finding arrives fully formed: the machine layer never files an anchorless finding, and
	// a source-less one would mean the dto dropped a field on the floor.
	for _, f := range resp.GetFindings() {
		require.Equal(t, techcardanalysis.SourceMachine, f.GetSource(), "audit returns machine findings only: %q", f.GetTitle())
		require.NotEmpty(t, f.GetTitle(), "a finding with no title")
		require.NotEmpty(t, f.GetSeverity(), "%q has no severity", f.GetTitle())
		require.NotEmpty(t, f.GetCategory(), "%q has no category", f.GetTitle())
		require.NotEmpty(t, f.GetRefs(), "%q has no anchors — the client cannot navigate to it", f.GetTitle())
	}

	// Fingerprints: one per numbered operation, keyed by the operation NUMBER.
	require.Len(t, resp.GetOperationFingerprints(), 2, "two numbered operations, two fingerprints: %v", resp.GetOperationFingerprints())
	require.Contains(t, resp.GetOperationFingerprints(), int32(10))
	require.Contains(t, resp.GetOperationFingerprints(), int32(20))
	require.NotEqual(t, resp.GetOperationFingerprints()[10], resp.GetOperationFingerprints()[20],
		"the two steps consume different things, so their shapes differ")

	require.NotEmpty(t, resp.GetNotChecked(), "the audit always says what it did not check")
	require.False(t, resp.GetAiEnabled(), "no OpenRouter client on this stand")
}

// TestGetTechCardConstructionAuditReportsAiEnabled pins the flag that wave 2's Analyze button reads.
// A client with a key answers true, and only the flag differs between the two runs.
func TestGetTechCardConstructionAuditReportsAiEnabled(t *testing.T) {
	off := tcaStand(t, tcaCard(), nil, map[string]decimal.Decimal{})
	respOff, err := off.GetTechCardConstructionAudit(context.Background(),
		&pb_admin.GetTechCardConstructionAuditRequest{TechCardId: 7})
	require.NoError(t, err)
	require.False(t, respOff.GetAiEnabled(), "an unconfigured deployment reports ai_enabled=false")

	on := tcaStand(t, tcaCard(), nil, map[string]decimal.Decimal{})
	on.aiOps = openrouter.New(openrouter.Config{APIKey: "k"})
	respOn, err := on.GetTechCardConstructionAudit(context.Background(),
		&pb_admin.GetTechCardConstructionAuditRequest{TechCardId: 7})
	require.NoError(t, err)
	require.True(t, respOn.GetAiEnabled(), "a configured deployment reports ai_enabled=true")
	require.Equal(t, tcaTitles(respOff), tcaTitles(respOn), "the key must not change a single machine finding")
}

// TestGetTechCardConstructionAuditOnAnEmptyCard: no operations is not an error and not a panic — an
// empty card is simply a card at the very beginning, and readiness has plenty to say about it.
func TestGetTechCardConstructionAuditOnAnEmptyCard(t *testing.T) {
	s := tcaStand(t, &entity.TechCard{Id: 7}, nil, map[string]decimal.Decimal{})
	resp, err := s.GetTechCardConstructionAudit(context.Background(),
		&pb_admin.GetTechCardConstructionAuditRequest{TechCardId: 7})
	require.NoError(t, err)
	require.Empty(t, resp.GetOperationFingerprints(), "no operations, no fingerprints")
	require.NotEmpty(t, resp.GetNotChecked())
}

// --- the FX plumbing -----------------------------------------------------------------------------

// TestGetTechCardConstructionAuditFeedsTheRatesToTheAnalyzer is the test that the wiring is real.
//
// entity.TechCard carries no currency rates and never will: they live in costing_fx_rate and the
// base currency in the boot cache, both of which only the handler can reach. So a handler that
// forgot to fetch them, or fetched them and passed an empty Fx, would still return a perfectly
// plausible audit — with one finding in it that is a pure artefact of the omission.
//
// The card is priced in PLN against a EUR base. Without a rate the money check says so; WITH the
// rate it must fall silent, and nothing else about the run may move.
func TestGetTechCardConstructionAuditFeedsTheRatesToTheAnalyzer(t *testing.T) {
	const noRateTitle = "PLN has no rate to EUR"

	without := tcaStand(t, tcaCard(), nil, map[string]decimal.Decimal{})
	respWithout, err := without.GetTechCardConstructionAudit(tcaCostingCtx(),
		&pb_admin.GetTechCardConstructionAuditRequest{TechCardId: 7})
	require.NoError(t, err)
	require.True(t, tcaHasTitleContaining(respWithout, noRateTitle),
		"with no PLN rate on file the audit must report the line dropping out of the total; got %v", tcaTitles(respWithout))

	with := tcaStand(t, tcaCard(), nil, map[string]decimal.Decimal{
		"PLN": decimal.RequireFromString("0.23"),
	})
	respWith, err := with.GetTechCardConstructionAudit(tcaCostingCtx(),
		&pb_admin.GetTechCardConstructionAuditRequest{TechCardId: 7})
	require.NoError(t, err)
	require.False(t, tcaHasTitleContaining(respWith, noRateTitle),
		"a PLN→EUR rate on file must silence the finding; got %v", tcaTitles(respWith))

	// The rate changes exactly ONE finding. Without this half the test would also pass against a
	// handler that fed the rates in and broke everything else in the process.
	strip := func(titles []string) []string {
		out := make([]string, 0, len(titles))
		for _, s := range titles {
			if strings.Contains(s, noRateTitle) {
				continue
			}
			out = append(out, s)
		}
		return out
	}
	require.Equal(t, strip(tcaTitles(respWithout)), tcaTitles(respWith),
		"the rate must move that one finding and nothing else")
}

// TestGetTechCardConstructionAuditSurvivesARateLoadFailure: a broken rate table degrades to no
// rates, exactly as s.costingFx does for every other tech-card read — it does not fail the audit.
// The whole machine layer would otherwise go dark over a currency table.
func TestGetTechCardConstructionAuditSurvivesARateLoadFailure(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(tcaCard(), nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(nil, errors.New("fx table is on fire"))
	s := &Server{repo: repo}

	resp, err := s.GetTechCardConstructionAudit(tcaCostingCtx(),
		&pb_admin.GetTechCardConstructionAuditRequest{TechCardId: 7})
	require.NoError(t, err, "a rate-load failure must not take the audit down with it")
	require.True(t, tcaHasTitleContaining(resp, "PLN has no rate to EUR"),
		"and the degradation must be SAID, not silently treated as a rate of one")
}

// --- the input gate ------------------------------------------------------------------------------

// tcaCardWithNOperations builds a card of exactly n numbered steps.
func tcaCardWithNOperations(n int) *entity.TechCard {
	card := &entity.TechCard{Id: 7}
	card.ApprovalState = entity.TechCardApprovalInReview
	card.Operations = make([]entity.TechCardOperation, 0, n)
	for i := 1; i <= n; i++ {
		card.Operations = append(card.Operations, entity.TechCardOperation{
			OperationNumber: sql.NullInt32{Int32: int32(i * 10), Valid: true},
			OperationType:   entity.OpTypeMachine,
			Zone:            entity.ZoneOuter,
			MachineType:     sql.NullString{String: "lockstitch", Valid: true},
		})
	}
	return card
}

// TestGetTechCardConstructionAuditInputGate pins the ceiling from BOTH sides: the largest card that
// is analysed, and the smallest that is refused.
//
// It refuses rather than truncates on purpose. An audit of the first 200 steps of a 260-step route
// would report "the route never packs" about a route that packs at step 240 — a confident, wrong
// verdict is worse than an honest refusal. (openrouter.maxOperations does truncate; it slices a
// generator's OUTPUT and is a different thing wearing a similar name.)
func TestGetTechCardConstructionAuditInputGate(t *testing.T) {
	t.Run("at the ceiling it runs", func(t *testing.T) {
		s := tcaStand(t, tcaCardWithNOperations(techcardanalysis.MaxAnalysisOperations), nil, map[string]decimal.Decimal{})
		resp, err := s.GetTechCardConstructionAudit(context.Background(),
			&pb_admin.GetTechCardConstructionAuditRequest{TechCardId: 7})
		require.NoError(t, err, "exactly %d operations is inside the gate", techcardanalysis.MaxAnalysisOperations)
		require.Len(t, resp.GetOperationFingerprints(), techcardanalysis.MaxAnalysisOperations)
	})

	t.Run("one over the ceiling is refused", func(t *testing.T) {
		// The stand forbids the rate table outright: refusing an oversized card must cost nothing.
		s := tcaStandThatForbidsFx(t, tcaCardWithNOperations(techcardanalysis.MaxAnalysisOperations+1))
		_, err := s.GetTechCardConstructionAudit(context.Background(),
			&pb_admin.GetTechCardConstructionAuditRequest{TechCardId: 7})
		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err), "an oversized card is a bad REQUEST, not a server fault")
		require.Contains(t, status.Convert(err).Message(), fmt.Sprintf("%d", techcardanalysis.MaxAnalysisOperations),
			"the refusal must name the ceiling, or the caller cannot tell how far over it is")
		require.Contains(t, status.Convert(err).Message(), fmt.Sprintf("%d operations", techcardanalysis.MaxAnalysisOperations+1),
			"and it must name what it actually got")
	})
}

// The gate's second copy of the ceiling is guarded by TestGetTechCardConstructionAuditInputGate
// itself, not by a separate assertion: both of its cards are sized from
// techcardanalysis.MaxAnalysisOperations, so a handler still holding a stale number refuses a card
// that is inside the analyzer's ceiling and the "at the ceiling it runs" case goes red. A test that
// merely restated `MaxAnalysisOperations == 200` could not fail for the reason it named — the
// mutation it existed to catch (a literal 200 in the handler) is behaviourally identical while the
// constant is 200, and stops being identical only in the case InputGate already covers.

// --- request validation --------------------------------------------------------------------------

// TestGetTechCardConstructionAuditRejectsABadId: no id, no read.
func TestGetTechCardConstructionAuditRejectsABadId(t *testing.T) {
	for _, id := range []int32{0, -1} {
		// No mocks at all: a handler that touched the repository before validating would panic here
		// rather than quietly pass.
		s := &Server{}
		_, err := s.GetTechCardConstructionAudit(context.Background(),
			&pb_admin.GetTechCardConstructionAuditRequest{TechCardId: id})
		require.Error(t, err, "tech_card_id=%d", id)
		require.Equal(t, codes.InvalidArgument, status.Code(err), "tech_card_id=%d", id)
	}
}

// TestGetTechCardConstructionAuditNotFound: a missing card is NotFound, not Internal — the client
// distinguishes "this card is gone" from "the server is broken".
func TestGetTechCardConstructionAuditNotFound(t *testing.T) {
	s := tcaStand(t, nil, sql.ErrNoRows, nil)
	_, err := s.GetTechCardConstructionAudit(context.Background(),
		&pb_admin.GetTechCardConstructionAuditRequest{TechCardId: 7})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err))
}

// TestGetTechCardConstructionAuditInternalOnAReadFailure: any other read failure is Internal, and
// the database's own sentence does not travel to the client.
func TestGetTechCardConstructionAuditInternalOnAReadFailure(t *testing.T) {
	s := tcaStand(t, nil, errors.New("connection reset by peer"), nil)
	_, err := s.GetTechCardConstructionAudit(context.Background(),
		&pb_admin.GetTechCardConstructionAuditRequest{TechCardId: 7})
	require.Error(t, err)
	require.Equal(t, codes.Internal, status.Code(err))
	require.NotContains(t, status.Convert(err).Message(), "connection reset by peer")
}

// --- RBAC ----------------------------------------------------------------------------------------

// TestGetTechCardConstructionAuditIsUnderTechCardRead is the RBAC half of the definition of done,
// and it exists because the failure mode is INVISIBLE TO THE AUTHOR.
//
// rbac.Authorize fails closed only for SCOPED accounts: a super or legacy token is allowed
// everything, including a method nobody classified. So a forgotten mapping ships a feature that
// works perfectly for whoever tested it and answers PermissionDenied for every scoped account —
// silently, and only in production.
//
// Four assertions, in the four states that matter: no grant at all, the read grant, the write grant
// (write covers read), and a grant on some OTHER section.
func TestGetTechCardConstructionAuditIsUnderTechCardRead(t *testing.T) {
	const method = rbac.MethodPrefix + "GetTechCardConstructionAudit"

	req, allowlisted, known := rbac.Lookup(method)
	require.True(t, known, "GetTechCardConstructionAudit is not classified: a scoped account would get PermissionDenied on it forever")
	require.False(t, allowlisted, "the audit must NOT be allowlisted — it reads a whole tech card")
	require.Equal(t, rbac.SectionTechCards, req.Section)
	require.Equal(t, entity.AccessRead, req.Access,
		"the machine audit spends nothing and writes nothing; read is the honest grant")

	scoped := func(section string, lvl entity.AccessLevel) map[string]entity.AccessLevel {
		return map[string]entity.AccessLevel{section: lvl}
	}
	for _, tc := range []struct {
		name  string
		perms map[string]entity.AccessLevel
		want  bool
	}{
		{"no grants at all", nil, false},
		{"tech_cards:read", scoped(rbac.SectionTechCards, entity.AccessRead), true},
		{"tech_cards:write covers read", scoped(rbac.SectionTechCards, entity.AccessWrite), true},
		{"another section's write is not this one", scoped(rbac.SectionProduction, entity.AccessWrite), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := rbac.Authorize(method, false, false, tc.perms)
			require.Equal(t, tc.want, got, "scoped account with %v", tc.perms)
		})
	}

	// A super token passes regardless — which is exactly why the assertions above cannot be replaced
	// by "we tried it and it worked".
	require.True(t, rbac.Authorize(method, false, true, nil), "super is allowed everything")
}

// --- costing redaction ---------------------------------------------------------------------------

// TestGetTechCardConstructionAuditRedactsMoneyWithoutCostingAccess is the acceptance test of the
// redaction, run on ONE card in BOTH directions.
//
// WHY IT EXISTS. The audit is rd(tech_cards); costing is a separate grant that redacts FIELDS rather
// than gating methods (stripTechCardCosting blanks BOM unit_price/currency out of GetTechCard). So
// the audience of this RPC is strictly wider than costing's, and B5а/B5б/B5в quote purchase prices
// and line currencies in prose — prose no field-level strip can reach. Without the filter this call
// is a side channel to exactly the numbers the neighbouring read hides from the same account.
//
// The second half is as load-bearing as the first: EVERY non-money finding must survive. Redaction
// that is merely "safe" is easy — return nothing — and it would take the whole feature away from the
// content manager it was supposed to serve.
func TestGetTechCardConstructionAuditRedactsMoneyWithoutCostingAccess(t *testing.T) {
	moneyTitles := []string{
		`Is "подкладка" priced or is that a placeholder?`,
		`"Карманка" costs more per metre than the main fabric`,
		"PLN has no rate to EUR",
	}

	withCosting := tcaStand(t, tcaMoneyCard(), nil, map[string]decimal.Decimal{})
	respWith, err := withCosting.GetTechCardConstructionAudit(tcaCostingCtx(),
		&pb_admin.GetTechCardConstructionAuditRequest{TechCardId: 7})
	require.NoError(t, err)

	// With the grant: the money findings are there, and nothing tells the reader anything was held.
	for _, want := range moneyTitles {
		require.True(t, tcaHasTitleContaining(respWith, want),
			"an account WITH costing:read must see %q; got %v", want, tcaTitles(respWith))
	}
	require.NotContains(t, respWith.GetNotChecked(), auditNoCostingAccessLine,
		"an account that saw the price checks must not be told they were not run")

	withoutCosting := tcaStand(t, tcaMoneyCard(), nil, map[string]decimal.Decimal{})
	respWithout, err := withoutCosting.GetTechCardConstructionAudit(tcaNoCostingCtx(),
		&pb_admin.GetTechCardConstructionAuditRequest{TechCardId: 7})
	require.NoError(t, err)

	// Without it: not one money finding, by title or by prose.
	for _, gone := range moneyTitles {
		require.False(t, tcaHasTitleContaining(respWithout, gone),
			"an account WITHOUT costing:read must not see %q; got %v", gone, tcaTitles(respWithout))
	}
	for _, f := range respWithout.GetFindings() {
		text := f.GetTitle() + " " + f.GetDetail() + " " + f.GetSuggestion() + " " + strings.Join(f.GetEvidence(), " ")
		for _, leak := range []string{" PLN", " EUR", "55", "60"} {
			require.NotContains(t, text, leak,
				"finding %q leaks a price or a currency to an account without costing:read: %q", f.GetTitle(), text)
		}
	}

	// And it is TOLD, not silently shortened: silence would read as "this card is clean on money".
	require.Contains(t, respWithout.GetNotChecked(), auditNoCostingAccessLine,
		"the withheld checks must be named in not_checked")

	// THE OTHER HALF: every non-money finding survives, by name. Without this the test would pass
	// against a handler that dropped the findings list altogether.
	kept := tcaTitles(respWithout)
	for _, title := range tcaTitles(respWith) {
		if tcaIsMoneyTitle(title, moneyTitles) {
			continue
		}
		require.Contains(t, kept, title,
			"non-money finding %q was collateral damage of the redaction", title)
	}
	require.Equal(t, len(tcaTitles(respWith))-len(moneyTitles), len(kept),
		"exactly the three money findings may disappear, no more and no fewer")

	// Fingerprints and ai_enabled are not money and are not touched.
	require.Equal(t, respWith.GetOperationFingerprints(), respWithout.GetOperationFingerprints(),
		"a fingerprint is a hash of an assembly shape, not money")
	require.Equal(t, respWith.GetAiEnabled(), respWithout.GetAiEnabled())
}

// TestGetTechCardConstructionAuditRedactsOnAContextThatNeverPassedTheInterceptor: an in-process
// caller with a bare context gets the REDACTED answer. costingAccessFor fails closed on a missing
// authz for exactly this reason, and the audit must inherit that rather than quietly opting out.
func TestGetTechCardConstructionAuditRedactsOnAContextThatNeverPassedTheInterceptor(t *testing.T) {
	s := tcaStand(t, tcaMoneyCard(), nil, map[string]decimal.Decimal{})
	resp, err := s.GetTechCardConstructionAudit(context.Background(),
		&pb_admin.GetTechCardConstructionAuditRequest{TechCardId: 7})
	require.NoError(t, err)
	require.False(t, tcaHasTitleContaining(resp, "PLN has no rate to EUR"),
		"a bare context must fail closed, like every other costing decision in this package")
	require.Contains(t, resp.GetNotChecked(), auditNoCostingAccessLine)
}

// TestAuditRedactionAnnouncesItselfEvenWithNothingToWithhold: the not_checked line goes in whenever
// the caller lacks the grant, card regardless. If it appeared only when something was actually
// withheld, its ABSENCE would tell the reader "this card has no money findings" — the same fact,
// leaked through the door left open by the fix.
func TestAuditRedactionAnnouncesItselfEvenWithNothingToWithhold(t *testing.T) {
	// tcaCard's only priced line is the PLN one; give the run a rate so B5в falls silent, and the
	// card then produces no money finding at all.
	s := tcaStand(t, tcaCard(), nil, map[string]decimal.Decimal{"PLN": decimal.RequireFromString("0.23")})
	resp, err := s.GetTechCardConstructionAudit(tcaNoCostingCtx(),
		&pb_admin.GetTechCardConstructionAuditRequest{TechCardId: 7})
	require.NoError(t, err)
	for _, f := range resp.GetFindings() {
		require.NotContains(t, f.GetTitle(), "PLN has no rate", "precondition: nothing to withhold on this run")
	}
	require.Contains(t, resp.GetNotChecked(), auditNoCostingAccessLine,
		"the line must not double as a signal that this particular card has money findings")
}

// tcaMoneyCard is tcaCard plus the two fabric lines that make B5а and B5б speak: a lining priced at
// a token 1 EUR, and a pocketing dearer per metre than the main cloth. Both PLN lines also keep B5в
// firing, so all three money checks are live on one card.
func tcaMoneyCard() *entity.TechCard {
	ns := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	ndd := func(s string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
	}
	card := tcaCard()
	card.BomItems = []entity.TechCardBomItem{
		{
			Id: 1, LineKey: "F1", Name: "основная", Section: entity.BomSectionFabric,
			Unit: ns("m"), UnitPrice: ndd("55"), Currency: ns("PLN"), Purpose: ns("main"),
		},
		{
			Id: 2, LineKey: "F2", Name: "Карманка", Section: entity.BomSectionFabric,
			Unit: ns("m"), UnitPrice: ndd("60"), Currency: ns("PLN"), Purpose: ns("pocketing"),
		},
		{
			Id: 3, LineKey: "L1", Name: "подкладка", Section: entity.BomSectionLining,
			Unit: ns("m"), UnitPrice: ndd("1"), Currency: ns("EUR"),
		},
	}
	return card
}

// tcaIsMoneyTitle reports whether a projected finding line is one of the money titles.
func tcaIsMoneyTitle(line string, moneyTitles []string) bool {
	for _, m := range moneyTitles {
		if strings.Contains(line, m) {
			return true
		}
	}
	return false
}

// --- the LLM layer: the stand ---------------------------------------------------------------------

// tcaModelCall is what the fake OpenRouter endpoint saw. The MODEL SLUG is recorded because the one
// thing this handler can get wrong invisibly is reporting a slug it never called.
type tcaModelCall struct {
	Model     string
	MaxTokens int
	JSONMode  bool
	User      string
}

// tcaFakeModel stands up a fake chat/completions endpoint and returns a client wired to it. cfg is
// the caller's (api key, model slugs); only BaseURL is overridden.
func tcaFakeModel(t *testing.T, cfg openrouter.Config, reply func(w http.ResponseWriter)) (*openrouter.Client, *[]tcaModelCall) {
	t.Helper()
	var mu sync.Mutex
	calls := make([]tcaModelCall, 0, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			Messages  []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			ResponseFormat *struct {
				Type string `json:"type"`
			} `json:"response_format"`
		}
		_ = json.Unmarshal(body, &req)
		call := tcaModelCall{Model: req.Model, MaxTokens: req.MaxTokens, JSONMode: req.ResponseFormat != nil}
		for _, m := range req.Messages {
			if m.Role == "user" {
				call.User = m.Content
			}
		}
		mu.Lock()
		calls = append(calls, call)
		mu.Unlock()
		reply(w)
	}))
	t.Cleanup(srv.Close)
	cfg.BaseURL = srv.URL
	if cfg.APIKey == "" {
		cfg.APIKey = "test-key"
	}
	return openrouter.New(cfg), &calls
}

// tcaModelAnswer replies with one OpenRouter envelope carrying `content` and `finishReason`. The
// usage numbers are non-zero so a test can tell "the bill was read" from "the field defaulted".
func tcaModelAnswer(content, finishReason string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": content},
				"finish_reason": finishReason,
			}},
			"usage": map[string]any{"prompt_tokens": 4321, "completion_tokens": 890, "total_tokens": 5211},
		})
	}
}

// tcaTranscript builds the JSON object §7.1 asks the model for.
func tcaTranscript(summary string, notChecked []string, findings ...map[string]any) string {
	if findings == nil {
		findings = []map[string]any{}
	}
	b, err := json.Marshal(map[string]any{
		"findings": findings, "not_checked": notChecked, "summary": summary,
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// tcaRouteFinding is a model finding that SURVIVES the verifier on tcaCard: it is anchored on the
// card as a whole, which is the one anchor the machine layer's own findings do not dedupe against
// (anchorSets drops "card" by construction), and its prose names no currency and no price word, so
// the money screen leaves it alone.
func tcaRouteFinding() map[string]any {
	return map[string]any{
		"category": "sequence", "severity": "warning",
		"title":      "Interlining is never fused before assembly",
		"detail":     "The route joins the front to the unit without a fusing step ahead of it.",
		"refs":       []string{"card"},
		"suggestion": "Add the fusing step before the first join.",
		"confidence": "likely",
	}
}

// tcaAnalysisStand is tcaStand plus a wired model client. The card read and the rate read are
// stubbed with .Maybe() on the id so a test may call the RPC several times (the belts are the
// subject of half of these tests).
func tcaAnalysisStand(t *testing.T, card *entity.TechCard, client *openrouter.Client) *Server {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc).Maybe()
	tc.EXPECT().GetTechCardById(mock.Anything, mock.Anything).Return(card, nil).Maybe()
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(map[string]decimal.Decimal{}, nil).Maybe()
	return &Server{repo: repo, aiOps: client}
}

// tcaAdminCtx is a scoped account WITH costing access, named. The name matters: raised_by and the
// per-admin rate window are both keyed on it.
func tcaAdminCtx(username string) context.Context {
	return authsrv.PutAdminUsername(tcaCostingCtx(), username)
}

// tcaAnalyze presses the button once.
func tcaAnalyze(ctx context.Context, s *Server, cardID int32) (*pb_admin.AnalyzeTechCardConstructionResponse, error) {
	return s.AnalyzeTechCardConstruction(ctx, &pb_admin.AnalyzeTechCardConstructionRequest{TechCardId: cardID})
}

// --- the LLM layer: statuses ----------------------------------------------------------------------

// TestAnalyzeTechCardConstructionHappyPath: a valid transcript comes back as model findings, ok, and
// the run's own fingerprint snapshot.
func TestAnalyzeTechCardConstructionHappyPath(t *testing.T) {
	client, calls := tcaFakeModel(t, openrouter.Config{}, tcaModelAnswer(
		tcaTranscript("The route assembles, but the interlining is never fused.",
			[]string{"stitch density (not stated on any step)"}, tcaRouteFinding()), "stop"))
	s := tcaAnalysisStand(t, tcaCard(), client)

	resp, err := tcaAnalyze(tcaAdminCtx("olga"), s, 7)
	require.NoError(t, err, "an AI run that worked must not be an RPC error")
	require.Equal(t, aiStatusOK, resp.GetAiStatus())
	require.Len(t, resp.GetFindings(), 1, "the one anchored finding must survive verification")
	require.Equal(t, techcardanalysis.SourceModel, resp.GetFindings()[0].GetSource(),
		"analyze returns model findings only — the machine ones are already on the screen")
	require.Equal(t, "Interlining is never fused before assembly", resp.GetFindings()[0].GetTitle())
	require.Contains(t, resp.GetSummary(), "interlining is never fused")
	require.Contains(t, resp.GetNotChecked(), "stitch density (not stated on any step)",
		"the model's own not-checked list must reach the caller")
	require.Len(t, resp.GetOperationFingerprints(), 2,
		"the run carries the fingerprints of the card it looked at: %v", resp.GetOperationFingerprints())

	require.Len(t, *calls, 1, "exactly one paid call per press")
	require.True(t, (*calls)[0].JSONMode, "the analysis prompt asks for a JSON object")
	require.Equal(t, analysisMaxTokens, (*calls)[0].MaxTokens, "the completion cap must actually be sent")
	require.Contains(t, (*calls)[0].User, "TECH CARD UNDER REVIEW", "the rendered §7.2 prompt is what was sent")
}

// TestAnalyzeTechCardConstructionModelHalfFailures walks every way the model half can fail and pins
// BOTH halves of the §4 inversion: a 200, and a status that names the fault.
//
// The inversion is the point. GenerateTechCardOperations answers FailedPrecondition without a key
// because it has nothing at all to return; here the machine section is already drawn, and turning
// the whole tab red over a retired slug would hide a working report behind a broken one.
func TestAnalyzeTechCardConstructionModelHalfFailures(t *testing.T) {
	t.Run("no key at all is not_configured, and nothing is called", func(t *testing.T) {
		client, calls := tcaFakeModel(t, openrouter.Config{APIKey: " "}, tcaModelAnswer("{}", "stop"))
		s := tcaAnalysisStand(t, tcaCard(), client)

		resp, err := tcaAnalyze(tcaAdminCtx("olga"), s, 7)
		require.NoError(t, err, "an unconfigured deployment must still answer 200 with the status")
		require.Equal(t, aiStatusNotConfigured, resp.GetAiStatus())
		require.Empty(t, resp.GetFindings())
		require.Empty(t, *calls, "a deployment with no key must not reach the provider at all")
		require.NotEmpty(t, resp.GetModel(), "the slug that WOULD be called is still named")
	})

	t.Run("a retired slug is model_unavailable and the slug is named", func(t *testing.T) {
		client, _ := tcaFakeModel(t, openrouter.Config{Model: "vendor/retired-model"},
			func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"message":"No endpoints found"}}`))
			})
		s := tcaAnalysisStand(t, tcaCard(), client)

		resp, err := tcaAnalyze(tcaAdminCtx("olga"), s, 7)
		require.NoError(t, err)
		require.Equal(t, aiStatusModelUnavailable, resp.GetAiStatus(),
			"a 404 is a configuration fault, not weather: it must never be reported as `failed`")
		require.Equal(t, "vendor/retired-model", resp.GetModel(),
			"the panel must name the slug to change; that is the whole difference from `failed`")
	})

	t.Run("a dead endpoint is failed", func(t *testing.T) {
		dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := dead.URL
		dead.Close() // nothing is listening any more: this is transport, i.e. weather
		s := tcaAnalysisStand(t, tcaCard(), openrouter.New(openrouter.Config{APIKey: "k", BaseURL: url}))

		resp, err := tcaAnalyze(tcaAdminCtx("olga"), s, 7)
		require.NoError(t, err)
		require.Equal(t, aiStatusFailed, resp.GetAiStatus())
	})

	t.Run("an answer cut by the token ceiling is invalid_output", func(t *testing.T) {
		client, _ := tcaFakeModel(t, openrouter.Config{}, tcaModelAnswer(
			tcaTranscript("", nil, tcaRouteFinding()), "length"))
		s := tcaAnalysisStand(t, tcaCard(), client)

		resp, err := tcaAnalyze(tcaAdminCtx("olga"), s, 7)
		require.NoError(t, err, "a truncated answer is still a 200 with a live machine section")
		require.Equal(t, aiStatusInvalidOutput, resp.GetAiStatus(),
			"finish_reason=length must beat the parser: this JSON happens to parse, and half a review served whole is the worst outcome")
		require.Empty(t, resp.GetFindings(), "the model half is discarded WHOLE, not partly")
	})

	t.Run("prose instead of JSON is invalid_output", func(t *testing.T) {
		client, _ := tcaFakeModel(t, openrouter.Config{},
			tcaModelAnswer("I had a look at the card and it seems fine.", "stop"))
		s := tcaAnalysisStand(t, tcaCard(), client)

		resp, err := tcaAnalyze(tcaAdminCtx("olga"), s, 7)
		require.NoError(t, err)
		require.Equal(t, aiStatusInvalidOutput, resp.GetAiStatus())
	})

	t.Run("a card with no assembly fact is skipped, unpaid", func(t *testing.T) {
		client, calls := tcaFakeModel(t, openrouter.Config{}, tcaModelAnswer(tcaTranscript("", nil), "stop"))
		// A card with no producing step at all: nothing was ever assembled, so there is no assembly
		// to judge and BuildUserPrompt refuses to build a run.
		s := tcaAnalysisStand(t, &entity.TechCard{Id: 7}, client)

		resp, err := tcaAnalyze(tcaAdminCtx("olga"), s, 7)
		require.NoError(t, err)
		require.Equal(t, aiStatusSkipped, resp.GetAiStatus())
		require.Empty(t, *calls, "an empty card must not be paid for")
	})
}

// TestAnalyzeReportsTheAnalysisSlugNotTheSharedOne is the T13а corollary, and it is the one defect
// this handler could ship completely silently.
//
// CompleteWithMeta sends AnalysisModel() — that is why OPENROUTER_MODEL_ANALYSIS exists at all. If
// the response reported Model() instead, then on the one deployment that sets the override the panel
// would name a slug nobody called, and whoever went to debug a 404 would go to the wrong knob. Both
// halves are asserted: what the provider RECEIVED and what the caller was TOLD, and they must agree.
func TestAnalyzeReportsTheAnalysisSlugNotTheSharedOne(t *testing.T) {
	client, calls := tcaFakeModel(t, openrouter.Config{
		Model: "shared/slug", ModelAnalysis: "analysis/slug",
	}, tcaModelAnswer(tcaTranscript("fine", nil, tcaRouteFinding()), "stop"))
	s := tcaAnalysisStand(t, tcaCard(), client)

	resp, err := tcaAnalyze(tcaAdminCtx("olga"), s, 7)
	require.NoError(t, err)
	require.Len(t, *calls, 1)
	require.Equal(t, "analysis/slug", (*calls)[0].Model, "precondition: the override is what gets sent")
	require.Equal(t, "analysis/slug", resp.GetModel(),
		"the reported model must be the one that was actually called, not the shared slug")
}

// --- the LLM layer: the three spend belts ---------------------------------------------------------

// TestAnalyzeInFlightBeltRefusesASecondRun: the double click. The first press is held inside the
// provider call; the second must be refused WHILE it is in the air — and must not reach the
// provider.
//
// Both waits are bounded, and that is not defensive dressing: a belt that is simply missing does not
// make the second press wrong, it makes it QUEUE behind the first one inside the paid call. Without
// the deadline that defect would look like a hung test instead of a named failure.
func TestAnalyzeInFlightBeltRefusesASecondRun(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(release) }) }

	client, calls := tcaFakeModel(t, openrouter.Config{}, func(w http.ResponseWriter) {
		entered <- struct{}{}
		<-release
		tcaModelAnswer(tcaTranscript("fine", nil, tcaRouteFinding()), "stop")(w)
	})
	// REGISTERED AFTER the fake server, so it runs BEFORE it: cleanups are LIFO, and httptest's own
	// Close waits for outstanding requests — which are the ones parked on this very channel. The
	// other order deadlocks the whole test binary the moment an assertion fails early.
	t.Cleanup(stop)
	s := tcaAnalysisStand(t, tcaCard(), client)
	ctx := tcaAdminCtx("olga")

	first := make(chan error, 1)
	go func() {
		_, err := tcaAnalyze(ctx, s, 7)
		first <- err
	}()
	select {
	case <-entered: // the first run is now inside the paid call
	case <-time.After(5 * time.Second):
		t.Fatal("the first run never reached the provider")
	}

	second := make(chan error, 1)
	go func() {
		_, err := tcaAnalyze(ctx, s, 7)
		second <- err
	}()
	var err error
	select {
	case err = <-second:
	case <-time.After(3 * time.Second):
		t.Fatal("a second press during a live run was neither refused nor answered: it queued behind the first one inside the paid call")
	}
	require.Error(t, err, "a second press while the first is running must be refused")
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "already running")

	stop()
	require.NoError(t, <-first, "the first run must finish normally")
	require.Len(t, *calls, 1, "the refused press must not have reached the provider")
}

// TestAnalyzePerCardIntervalBelt: pressing the same card again immediately after a run is refused,
// and the refusal costs nothing.
func TestAnalyzePerCardIntervalBelt(t *testing.T) {
	client, calls := tcaFakeModel(t, openrouter.Config{},
		tcaModelAnswer(tcaTranscript("fine", nil, tcaRouteFinding()), "stop"))
	s := tcaAnalysisStand(t, tcaCard(), client)
	ctx := tcaAdminCtx("olga")

	resp, err := tcaAnalyze(ctx, s, 7)
	require.NoError(t, err)
	require.Equal(t, aiStatusOK, resp.GetAiStatus())

	_, err = tcaAnalyze(ctx, s, 7)
	require.Error(t, err, "the same card again, seconds later, is drumming on the button")
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Len(t, *calls, 1, "the refused press must not have been paid for")

	// ANOTHER CARD IS NOT BLOCKED. Without this the test would also pass against a belt that simply
	// froze the whole account for 15 seconds — a different, much worse product.
	_, err = tcaAnalyze(ctx, s, 8)
	require.NoError(t, err, "the interval is per card; a different card is ordinary work")
}

// TestAnalyzeRefusalCostsNoCardRead pins the ORDER of the handler, which is the part of it that is
// invisible in every response.
//
// The belts stand before the card is read, not merely before the model is called. A hydrated
// tech-card read is the most expensive query on this path, and a press that is going to be refused
// must cost nothing at all — otherwise a person leaning on the button still loads the whole card
// twenty times a minute while being told to wait.
//
// The stand allows EXACTLY ONE read, so a handler that read first and refused after fails by name.
func TestAnalyzeRefusalCostsNoCardRead(t *testing.T) {
	client, _ := tcaFakeModel(t, openrouter.Config{},
		tcaModelAnswer(tcaTranscript("fine", nil, tcaRouteFinding()), "stop"))
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc).Maybe()
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(tcaCard(), nil).Once()
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(map[string]decimal.Decimal{}, nil).Maybe()
	s := &Server{repo: repo, aiOps: client}
	ctx := tcaAdminCtx("olga")

	_, err := tcaAnalyze(ctx, s, 7)
	require.NoError(t, err)

	_, err = tcaAnalyze(ctx, s, 7)
	require.Equal(t, codes.ResourceExhausted, status.Code(err),
		"the second press is refused by the per-card interval")
}

// TestAnalyzePerCardIntervalExpires: the belt RELEASES. A guard that never forgot a card would look
// identical to a correct one in every test that only presses twice, and would take the feature away
// from the technologist who fixed the card and wants to re-run it.
func TestAnalyzePerCardIntervalExpires(t *testing.T) {
	client, calls := tcaFakeModel(t, openrouter.Config{},
		tcaModelAnswer(tcaTranscript("fine", nil, tcaRouteFinding()), "stop"))
	s := tcaAnalysisStand(t, tcaCard(), client)
	now := time.Now()
	s.analysisRuns.nowFn = func() time.Time { return now }
	ctx := tcaAdminCtx("olga")

	_, err := tcaAnalyze(ctx, s, 7)
	require.NoError(t, err)

	now = now.Add(analysisMinInterval - time.Second)
	_, err = tcaAnalyze(ctx, s, 7)
	require.Equal(t, codes.ResourceExhausted, status.Code(err), "one second short of the interval is still inside it")

	now = now.Add(2 * time.Second)
	_, err = tcaAnalyze(ctx, s, 7)
	require.NoError(t, err, "past the interval the card is analysable again")
	require.Len(t, *calls, 2)
}

// TestAnalyzePerAdminHourlyBelt: the belt the OTHER two cannot substitute for. A card is not a free
// multiplier of the key — twenty-one cards in an hour is twenty-one bills, and every one of them
// passes the in-flight map and the per-card interval untouched.
func TestAnalyzePerAdminHourlyBelt(t *testing.T) {
	client, calls := tcaFakeModel(t, openrouter.Config{},
		tcaModelAnswer(tcaTranscript("fine", nil, tcaRouteFinding()), "stop"))
	s := tcaAnalysisStand(t, tcaCard(), client)
	ctx := tcaAdminCtx("olga")

	for i := 1; i <= analysisPerAdminRuns; i++ {
		_, err := tcaAnalyze(ctx, s, int32(100+i)) // a different card every time
		require.NoError(t, err, "run %d of %d is inside the hourly window", i, analysisPerAdminRuns)
	}
	_, err := tcaAnalyze(ctx, s, 999)
	require.Error(t, err, "run %d must be refused", analysisPerAdminRuns+1)
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Len(t, *calls, analysisPerAdminRuns, "the refused run must not have been paid for")

	// THE WINDOW IS PER ADMIN. Without this half the test would pass against a global counter, which
	// would let one busy technologist lock the feature for everybody else.
	_, err = tcaAnalyze(tcaAdminCtx("marina"), s, 999)
	require.NoError(t, err, "another account has its own window")
}

// --- the LLM layer: the run log -------------------------------------------------------------------

// tcaLogRecord is one captured slog record, flattened to what the assertions need.
type tcaLogRecord struct {
	Level slog.Level
	Attrs map[string]string
}

// tcaLogSink captures records instead of printing them.
type tcaLogSink struct {
	mu      sync.Mutex
	records []tcaLogRecord
}

func (h *tcaLogSink) Enabled(context.Context, slog.Level) bool { return true }
func (h *tcaLogSink) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *tcaLogSink) WithGroup(string) slog.Handler            { return h }
func (h *tcaLogSink) Handle(_ context.Context, r slog.Record) error {
	rec := tcaLogRecord{Level: r.Level, Attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.Attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, rec)
	h.mu.Unlock()
	return nil
}

// tcaCaptureLog swaps the default logger for the duration of one test.
func tcaCaptureLog(t *testing.T) *tcaLogSink {
	t.Helper()
	sink := &tcaLogSink{}
	prev := slog.Default()
	slog.SetDefault(slog.New(sink))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return sink
}

func (h *tcaLogSink) errors() []tcaLogRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]tcaLogRecord, 0, len(h.records))
	for _, r := range h.records {
		if r.Level == slog.LevelError {
			out = append(out, r)
		}
	}
	return out
}

// TestAnalyzeLogsEveryNonOkRunOnce is §8 п.9 with a probe, because a gate item asserted by nobody is
// not a gate item.
//
// The run log is the ONLY diagnosis a failed press ever leaves: the RPC answers 200 with a status
// word, and the provider's own sentence, the base URL and the token bill live here or nowhere. One
// record per failure, at Error, so "how often did analysis fail last week" is a count.
func TestAnalyzeLogsEveryNonOkRunOnce(t *testing.T) {
	for _, tc := range []struct {
		name   string
		want   string
		server func(t *testing.T) *Server
	}{
		{"not_configured", aiStatusNotConfigured, func(t *testing.T) *Server {
			return tcaAnalysisStand(t, tcaCard(), openrouter.New(openrouter.Config{}))
		}},
		{"model_unavailable", aiStatusModelUnavailable, func(t *testing.T) *Server {
			client, _ := tcaFakeModel(t, openrouter.Config{}, func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusNotFound)
			})
			return tcaAnalysisStand(t, tcaCard(), client)
		}},
		{"failed", aiStatusFailed, func(t *testing.T) *Server {
			dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			url := dead.URL
			dead.Close()
			return tcaAnalysisStand(t, tcaCard(), openrouter.New(openrouter.Config{APIKey: "k", BaseURL: url}))
		}},
		{"invalid_output", aiStatusInvalidOutput, func(t *testing.T) *Server {
			client, _ := tcaFakeModel(t, openrouter.Config{}, tcaModelAnswer("not json at all", "stop"))
			return tcaAnalysisStand(t, tcaCard(), client)
		}},
		// The whole completion budget spent on thinking, nothing said. This is the one that
		// reached production: it must NOT land in `failed`, whose sentence offers a retry.
		{"budget_exhausted", aiStatusBudgetExhausted, func(t *testing.T) *Server {
			client, _ := tcaFakeModel(t, openrouter.Config{}, tcaModelAnswer("", "length"))
			return tcaAnalysisStand(t, tcaCard(), client)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := tcaCaptureLog(t)
			s := tc.server(t)

			resp, err := tcaAnalyze(tcaAdminCtx("olga"), s, 7)
			require.NoError(t, err)
			require.Equal(t, tc.want, resp.GetAiStatus(), "precondition: this stand produces %s", tc.want)

			errs := sink.errors()
			require.Len(t, errs, 1, "a non-ok run leaves EXACTLY one Error record, no more and no fewer: %+v", errs)
			require.Equal(t, tc.want, errs[0].Attrs["ai_status"], "the record must carry the status: %+v", errs[0].Attrs)
			require.NotEmpty(t, errs[0].Attrs["model"], "the record must name the model: %+v", errs[0].Attrs)
			require.Contains(t, errs[0].Attrs, "base_url",
				"a 404 can mean a dead slug OR a base url pointing nowhere; a log naming only the slug sends the reader to the wrong knob")
			require.Contains(t, errs[0].Attrs, "finish_reason",
				"the provider's own word for why the completion stopped is what separates «spent the budget» from «said something broken»; without it the first production failure had to be reconstructed from the token counts printed beside it: %+v", errs[0].Attrs)
		})
	}
}

// TestAnalyzeLogsTheBillOfASuccessfulRun: a per-press call to a paid API whose cost never reaches
// the log is a bill nobody can see (§12). A clean run is NOT an Error — that is the other half.
func TestAnalyzeLogsTheBillOfASuccessfulRun(t *testing.T) {
	sink := tcaCaptureLog(t)
	client, _ := tcaFakeModel(t, openrouter.Config{},
		tcaModelAnswer(tcaTranscript("fine", nil, tcaRouteFinding()), "stop"))
	s := tcaAnalysisStand(t, tcaCard(), client)

	_, err := tcaAnalyze(tcaAdminCtx("olga"), s, 7)
	require.NoError(t, err)
	require.Empty(t, sink.errors(), "a run that worked is not an error")

	sink.mu.Lock()
	defer sink.mu.Unlock()
	var billed bool
	for _, r := range sink.records {
		if r.Attrs["total_tokens"] == "5211" {
			billed = true
			require.Equal(t, "4321", r.Attrs["prompt_tokens"])
			require.Equal(t, "890", r.Attrs["completion_tokens"])
		}
	}
	require.True(t, billed, "the token usage of the run must be in the log: %+v", sink.records)
}

// --- the LLM layer: money -------------------------------------------------------------------------

// tcaMoneyModelFinding is a model finding that quotes a purchase price in prose. Nothing about it is
// a "money check" by name — it is an ordinary sequence finding whose text happens to name the price,
// which is exactly the shape §12 says must not slip through.
func tcaMoneyModelFinding() map[string]any {
	return map[string]any{
		"category": "bom_mismatch", "severity": "warning",
		"title":      "The pocketing is dearer per metre than the shell",
		"detail":     "The pocketing line is 60 PLN per metre against 55 PLN for the main cloth.",
		"refs":       []string{"card"},
		"confidence": "likely",
	}
}

// TestAnalyzeRedactsModelMoneyFindings is the wave-2 half of the boundary closed in T6 for the
// machine layer.
//
// The prompt puts purchase prices in the context ON PURPOSE (without them a whole class of finding
// disappears), so the model can quote one in ANY finding it writes. The verifier flags those, and
// this handler must drop them for an account holding tech_cards:read without costing:read — the very
// account GetTechCard serves with unit_price blanked out. Inheriting "model findings are not money"
// silently would reopen the hole under a new name.
func TestAnalyzeRedactsModelMoneyFindings(t *testing.T) {
	transcript := tcaTranscript("The route looks sound.", nil, tcaRouteFinding(), tcaMoneyModelFinding())

	withCosting, _ := tcaFakeModel(t, openrouter.Config{}, tcaModelAnswer(transcript, "stop"))
	respWith, err := tcaAnalyze(tcaAdminCtx("olga"), tcaAnalysisStand(t, tcaCard(), withCosting), 7)
	require.NoError(t, err)
	require.Equal(t, aiStatusOK, respWith.GetAiStatus())
	require.True(t, tcaHasAnalysisTitle(respWith, "dearer per metre"),
		"precondition: an account WITH costing:read sees the priced finding; got %v", tcaAnalysisTitles(respWith))
	require.NotContains(t, respWith.GetNotChecked(), auditNoCostingAccessLine,
		"an account that saw the prices must not be told they were withheld")

	withoutCosting, _ := tcaFakeModel(t, openrouter.Config{}, tcaModelAnswer(transcript, "stop"))
	respWithout, err := tcaAnalyze(authsrv.PutAdminUsername(tcaNoCostingCtx(), "olga"),
		tcaAnalysisStand(t, tcaCard(), withoutCosting), 7)
	require.NoError(t, err)
	require.Equal(t, aiStatusOK, respWithout.GetAiStatus())
	require.False(t, tcaHasAnalysisTitle(respWithout, "dearer per metre"),
		"a model finding that quotes a purchase price must not reach an account without costing:read; got %v",
		tcaAnalysisTitles(respWithout))
	require.Contains(t, respWithout.GetNotChecked(), auditNoCostingAccessLine,
		"and the account must be TOLD, or the silence reads as `this card is clean on money`")

	// THE OTHER HALF: the non-money finding survives. Redaction that returns nothing is easy and
	// takes the whole feature away from the reader it was built for.
	require.True(t, tcaHasAnalysisTitle(respWithout, "Interlining is never fused"),
		"the finding that says nothing about money was collateral damage: %v", tcaAnalysisTitles(respWithout))
}

// TestAnalyzeWithholdsTheModelsProseWithoutCostingAccess closes the hole redactMoneyFindings CANNOT
// reach.
//
// Finding.Money is a flag on a finding. `summary` and the model's `not_checked` lines are neither
// findings nor fields — there is nowhere on them to put a flag — and a model that writes «the
// pocketing at 60 PLN is dearer than the shell» into the summary ships exactly the RATIO the
// redaction exists to hide, while the findings half of the same response is correctly clean.
//
// The assertion is over the WHOLE marshalled response on purpose: a check that only walked the
// findings would have passed against the very bug this test exists for.
func TestAnalyzeWithholdsTheModelsProseWithoutCostingAccess(t *testing.T) {
	const leak = "the pocketing at 60 PLN per metre is dearer than the shell at 55 PLN"
	transcript := tcaTranscript(leak,
		[]string{"trim prices (the trims are quoted at 12 PLN and were not compared)"}, tcaRouteFinding())

	client, _ := tcaFakeModel(t, openrouter.Config{}, tcaModelAnswer(transcript, "stop"))
	resp, err := tcaAnalyze(authsrv.PutAdminUsername(tcaNoCostingCtx(), "olga"),
		tcaAnalysisStand(t, tcaCard(), client), 7)
	require.NoError(t, err)
	require.Equal(t, aiStatusOK, resp.GetAiStatus())

	raw, err := protojson.Marshal(resp)
	require.NoError(t, err)
	body := string(raw)
	for _, forbidden := range []string{"PLN", "dearer", "per metre"} {
		require.NotContains(t, body, forbidden,
			"the model's prose leaked %q to an account without costing:read: %s", forbidden, body)
	}
	require.Contains(t, resp.GetNotChecked(), auditNoCostingAccessLine,
		"the always-present line must still be there when the grant is missing")
	require.NotEmpty(t, resp.GetSummary(),
		"a blank summary would read as `the model had nothing to say`; the reader is told why it is gone")

	// The same run, WITH the grant, delivers the prose untouched — otherwise this test would pass
	// against a handler that simply never returns a summary.
	client2, _ := tcaFakeModel(t, openrouter.Config{}, tcaModelAnswer(transcript, "stop"))
	respWith, err := tcaAnalyze(tcaAdminCtx("olga"), tcaAnalysisStand(t, tcaCard(), client2), 7)
	require.NoError(t, err)
	require.Contains(t, respWith.GetSummary(), "60 PLN", "an account with costing:read reads the model's own words")
	require.Contains(t, respWith.GetNotChecked()[0], "trim prices")
}

// tcaAnalysisTitles / tcaHasAnalysisTitle project the analyse response down to its finding titles.
func tcaAnalysisTitles(resp *pb_admin.AnalyzeTechCardConstructionResponse) []string {
	out := make([]string, 0, len(resp.GetFindings()))
	for _, f := range resp.GetFindings() {
		out = append(out, f.GetTitle())
	}
	return out
}

func tcaHasAnalysisTitle(resp *pb_admin.AnalyzeTechCardConstructionResponse, sub string) bool {
	for _, f := range resp.GetFindings() {
		if strings.Contains(f.GetTitle(), sub) {
			return true
		}
	}
	return false
}

// --- the LLM layer: the input gate ----------------------------------------------------------------

// TestAnalyzeRefusesAnOversizedCard: the same ceiling as the audit, refused the same way. An
// analysis of the first 200 steps of a 260-step route would be a confident verdict about a route it
// never saw the end of.
func TestAnalyzeRefusesAnOversizedCard(t *testing.T) {
	client, calls := tcaFakeModel(t, openrouter.Config{}, tcaModelAnswer(tcaTranscript("", nil), "stop"))
	s := tcaAnalysisStand(t, tcaCardWithNOperations(techcardanalysis.MaxAnalysisOperations+1), client)

	_, err := tcaAnalyze(tcaAdminCtx("olga"), s, 7)
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err), "an oversized card is a bad REQUEST, not an ai_status")
	require.Empty(t, *calls, "and it is refused before anything is spent")
}

// TestAnalyzeRejectsABadId: no id, no run — and no mocks at all, so a handler that read the
// repository first would panic here rather than quietly pass.
func TestAnalyzeRejectsABadId(t *testing.T) {
	for _, id := range []int32{0, -1} {
		_, err := tcaAnalyze(context.Background(), &Server{}, id)
		require.Error(t, err, "tech_card_id=%d", id)
		require.Equal(t, codes.InvalidArgument, status.Code(err), "tech_card_id=%d", id)
	}
}

// --- RBAC -----------------------------------------------------------------------------------------

// TestWaveTwoMethodsAreUnderTechCardWrite classifies both new methods, and the failure mode it
// guards is invisible to whoever wrote them: rbac.Authorize fails closed only for SCOPED accounts,
// so a forgotten mapping ships a feature that works perfectly for the author's super token and
// answers PermissionDenied for every scoped account, in production only.
//
// WRITE, not read, and for two different reasons. AddTechCardIssue writes a row. Analyze writes
// nothing at all — but a press SPENDS THE KEY, and a grant to spend is an authoring grant. The
// precedent is GenerateTechCardOperations, classified wr for exactly that argument.
func TestWaveTwoMethodsAreUnderTechCardWrite(t *testing.T) {
	for _, name := range []string{"AnalyzeTechCardConstruction", "AddTechCardIssue"} {
		t.Run(name, func(t *testing.T) {
			method := rbac.MethodPrefix + name
			req, allowlisted, known := rbac.Lookup(method)
			require.True(t, known, "%s is not classified: every scoped account would get PermissionDenied on it forever", name)
			require.False(t, allowlisted, "%s must not be allowlisted", name)
			require.Equal(t, rbac.SectionTechCards, req.Section)
			require.Equal(t, entity.AccessWrite, req.Access,
				"%s is an authoring grant: it spends the key or writes a row", name)

			scoped := func(section string, lvl entity.AccessLevel) map[string]entity.AccessLevel {
				return map[string]entity.AccessLevel{section: lvl}
			}
			require.False(t, rbac.Authorize(method, false, false, nil), "no grants at all")
			require.False(t, rbac.Authorize(method, false, false, scoped(rbac.SectionTechCards, entity.AccessRead)),
				"tech_cards:READ must not be enough — that is the whole difference from the machine audit")
			require.True(t, rbac.Authorize(method, false, false, scoped(rbac.SectionTechCards, entity.AccessWrite)))
			require.False(t, rbac.Authorize(method, false, false, scoped(rbac.SectionProduction, entity.AccessWrite)),
				"another section's write is not this one")
			require.True(t, rbac.Authorize(method, false, true, nil), "super is allowed everything")
		})
	}
}

// --- filing an issue ------------------------------------------------------------------------------

// tcaIssueStand wires a Server whose TechCards mock expects the NARROW filing call and NOTHING ELSE.
// That is an assertion, not a convenience: mockery fails an unexpected call, so a handler that read
// or rewrote the card on this path — which is what makes filing impossible on a released card —
// fails the test by name.
func tcaIssueStand(t *testing.T, id int, err error) (*Server, *entity.TechCardIssue, *int) {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	var got entity.TechCardIssue
	var gotCard int
	tc.EXPECT().AddTechCardIssue(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, cardID int, issue entity.TechCardIssue) (int, error) {
			gotCard, got = cardID, issue
			return id, err
		})
	if err != nil {
		repo.EXPECT().IsErrForeignKeyViolation(mock.Anything).Return(true).Maybe()
	}
	return &Server{repo: repo}, &got, &gotCard
}

// TestAddTechCardIssueStampsTheAuthorFromTheToken: the row is filed, the author comes from the JWT,
// and the card is never read or rewritten — which is what lets this work on a released card.
func TestAddTechCardIssueStampsTheAuthorFromTheToken(t *testing.T) {
	s, got, gotCard := tcaIssueStand(t, 42, nil)
	ctx := authsrv.PutAdminUsername(context.Background(), "olga")

	resp, err := s.AddTechCardIssue(ctx, &pb_admin.AddTechCardIssueRequest{
		TechCardId: 7, OperationNumber: 460, Severity: "HIGH",
		Description: "  the underarm cannot be closed in this order  ",
	})
	require.NoError(t, err)
	require.Equal(t, int32(42), resp.GetIssueId(), "the new row's id comes back so the client need not re-read the card")
	require.Equal(t, 7, *gotCard)
	require.Equal(t, "olga", got.RaisedBy.String,
		"raised_by comes from the token; UpdateTechCard passes the CLIENT's value through and is not the model to copy here")
	require.True(t, got.RaisedBy.Valid)
	require.Equal(t, entity.IssueSeverityHigh, got.Severity, "HIGH on the wire is `high` in the column (0072 CHECK)")
	require.Equal(t, entity.IssueStatusOpen, got.Status, "a newly filed issue is open by definition")
	require.Equal(t, "the underarm cannot be closed in this order", got.Description, "the text is trimmed")
	require.Equal(t, int32(460), got.OperationNumber.Int32)
	require.True(t, got.OperationNumber.Valid)
}

// TestAddTechCardIssueWithoutAnOperationStoresNoLink: 0 means "about the card", and it is stored as
// NO LINK rather than as step zero — a row pointing at operation 0 would be a link to a step that
// cannot exist, and every reader joining on the number would have to know that one number lies.
func TestAddTechCardIssueWithoutAnOperationStoresNoLink(t *testing.T) {
	s, got, _ := tcaIssueStand(t, 1, nil)
	_, err := s.AddTechCardIssue(authsrv.PutAdminUsername(context.Background(), "olga"),
		&pb_admin.AddTechCardIssueRequest{TechCardId: 7, Severity: "low", Description: "the sketch does not match"})
	require.NoError(t, err)
	require.False(t, got.OperationNumber.Valid, "operation_number 0 must be stored as NULL, not as step zero")
	require.Equal(t, entity.IssueSeverityLow, got.Severity, "the severity token is matched case-insensitively")
}

// TestAddTechCardIssueRefusesUnusableInput. No mocks: a handler that wrote first and validated after
// would panic here instead of quietly passing.
func TestAddTechCardIssueRefusesUnusableInput(t *testing.T) {
	for name, req := range map[string]*pb_admin.AddTechCardIssueRequest{
		"no card":          {TechCardId: 0, Severity: "HIGH", Description: "x"},
		"no description":   {TechCardId: 7, Severity: "HIGH", Description: "   "},
		"unknown severity": {TechCardId: 7, Severity: "CRITICAL", Description: "x"},
		"negative step":    {TechCardId: 7, OperationNumber: -1, Severity: "HIGH", Description: "x"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := (&Server{}).AddTechCardIssue(context.Background(), req)
			require.Error(t, err)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

// TestAddTechCardIssueOnAMissingCardIsNotFound: tech_card_id is the only foreign key on the row, so
// a violation says exactly one thing and NotFound says it — the same sentence the audit gives for
// the same card.
func TestAddTechCardIssueOnAMissingCardIsNotFound(t *testing.T) {
	s, _, _ := tcaIssueStand(t, 0, errors.New("Error 1452: a foreign key constraint fails"))
	_, err := s.AddTechCardIssue(authsrv.PutAdminUsername(context.Background(), "olga"),
		&pb_admin.AddTechCardIssueRequest{TechCardId: 7, Severity: "HIGH", Description: "x"})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err))
	require.NotContains(t, status.Convert(err).Message(), "1452", "a person reads a sentence, not a MySQL error number")
}

// tcaFakeResult is a sql.Result that reports one inserted row.
type tcaFakeResult struct{ id int64 }

func (r tcaFakeResult) LastInsertId() (int64, error) { return r.id, nil }
func (r tcaFakeResult) RowsAffected() (int64, error) { return 1, nil }

// TestAddTechCardIssueFilesAtTheEndOfTheList pins the one property of the store statement the
// handler cannot express: display_order.
//
// The column is NOT NULL DEFAULT 0 (0072). An INSERT that simply omitted it would file every new
// issue at position zero, so each newest issue would land at the TOP of a list read in
// display_order, tied with all its predecessors — a defect that shows up as "the list is in a
// strange order", weeks later, on somebody else's screen.
//
// It runs against a mock DB rather than a database: the position is decided by the STATEMENT, and
// the statement is observable here without a schema. (The store package's own tests are not part of
// this feature's gate.)
func TestAddTechCardIssueFilesAtTheEndOfTheList(t *testing.T) {
	db := mocks.NewMockDB(t)
	var query string
	// Nine bound parameters, so nine matchers: the mock matches variadics positionally, and a
	// count that drifts from the statement fails here loudly rather than silently matching nothing.
	anyArgs := make([]interface{}, 9)
	for i := range anyArgs {
		anyArgs[i] = mock.Anything
	}
	db.EXPECT().ExecContext(mock.Anything, mock.Anything, anyArgs...).
		RunAndReturn(func(_ context.Context, q string, _ ...interface{}) (sql.Result, error) {
			query = q
			return tcaFakeResult{id: 42}, nil
		})
	store := techcard.New(storeutil.Base{DB: db}, nil, nil, nil)

	id, err := store.AddTechCardIssue(context.Background(), 7, entity.TechCardIssue{
		Severity: entity.IssueSeverityHigh, Status: entity.IssueStatusOpen, Description: "x",
	})
	require.NoError(t, err)
	require.Equal(t, 42, id)
	require.Contains(t, query, "INSERT INTO tech_card_issue")
	require.Contains(t, strings.Join(strings.Fields(query), " "), "COALESCE(MAX(i.display_order), -1) + 1",
		"display_order must be max+1 within the card, computed in the statement: %s", query)
}
