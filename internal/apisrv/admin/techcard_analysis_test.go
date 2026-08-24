package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	"github.com/jekabolt/grbpwr-manager/internal/techcardanalysis"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	// .Maybe(): the input gate refuses an oversized card BEFORE any rate is needed, and that test
	// asserts exactly that by way of this expectation never being required.
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(rates, nil).Maybe()
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

// --- happy path ----------------------------------------------------------------------------------

// TestGetTechCardConstructionAuditHappyPath: a real card comes back as findings + a fingerprint per
// numbered operation + the not-checked list, all of it converted onto the wire.
func TestGetTechCardConstructionAuditHappyPath(t *testing.T) {
	s := tcaStand(t, tcaCard(), nil, map[string]decimal.Decimal{})
	resp, err := s.GetTechCardConstructionAudit(context.Background(),
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
	respWithout, err := without.GetTechCardConstructionAudit(context.Background(),
		&pb_admin.GetTechCardConstructionAuditRequest{TechCardId: 7})
	require.NoError(t, err)
	require.True(t, tcaHasTitleContaining(respWithout, noRateTitle),
		"with no PLN rate on file the audit must report the line dropping out of the total; got %v", tcaTitles(respWithout))

	with := tcaStand(t, tcaCard(), nil, map[string]decimal.Decimal{
		"PLN": decimal.RequireFromString("0.23"),
	})
	respWith, err := with.GetTechCardConstructionAudit(context.Background(),
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

	resp, err := s.GetTechCardConstructionAudit(context.Background(),
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
		s := tcaStand(t, tcaCardWithNOperations(techcardanalysis.MaxAnalysisOperations+1), nil, map[string]decimal.Decimal{})
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

// TestGetTechCardConstructionAuditGateReadsTheAnalyzersCeiling guards against the second copy of the
// number. The gate must read techcardanalysis.MaxAnalysisOperations, not a local 200 that will one
// day disagree with it; the ceiling is exported precisely so there is only one.
func TestGetTechCardConstructionAuditGateReadsTheAnalyzersCeiling(t *testing.T) {
	require.Equal(t, 200, techcardanalysis.MaxAnalysisOperations,
		"if the analyzer's ceiling moved, this test is the reminder that the handler's refusal text moves with it")
}

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
