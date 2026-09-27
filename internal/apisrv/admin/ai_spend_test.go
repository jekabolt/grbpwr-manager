package admin

import (
	"context"
	"errors"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// aiSpendServer wires a Server whose store answers SpendReport(from, to) with rep, exactly once.
func aiSpendServer(t *testing.T, from, to string, rep *entity.AISpendReport, err error) *Server {
	t.Helper()
	ai := mocks.NewMockAI(t)
	ai.EXPECT().SpendReport(mock.Anything, from, to).Return(rep, err).Once()
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().AI().Return(ai)
	return &Server{repo: repo}
}

func spendUSD(v string) decimal.NullDecimal {
	return decimal.NewNullDecimal(decimal.RequireFromString(v))
}

// fieldOf returns the one BadRequest field violation an InvalidArgument carries.
func fieldOf(t *testing.T, err error) *errdetails.BadRequest_FieldViolation {
	t.Helper()
	require.Equal(t, codes.InvalidArgument, status.Code(err), "err = %v", err)
	for _, d := range status.Convert(err).Details() {
		if br, ok := d.(*errdetails.BadRequest); ok {
			require.Len(t, br.FieldViolations, 1)
			return br.FieldViolations[0]
		}
	}
	t.Fatalf("no BadRequest detail on %v", err)
	return nil
}

// TestGetAiSpendReportRefusesABadPeriodBeforeTheStore.
//
// The repository mock has NO expectations: mockery fails the test on any call, so every refusal below
// is proven to happen before the ledger is asked anything.
//
// MUTATIONS IT CATCHES: the span check dropped (367 days reach the store); the reversed check dropped
// (from > to reaches the store); a day's parse error ignored (the case reads as another refusal or
// reaches the store, and the field/reason assertions go red); the bound counted as a difference
// instead of inclusive days (366 inclusive days refused, or 367 let through).
func TestGetAiSpendReportRefusesABadPeriodBeforeTheStore(t *testing.T) {
	for name, c := range map[string]struct {
		from, to      string
		field, reason string
	}{
		"from not a day":         {"2026-9-1", "2026-09-27", "from_day", "bad_day"},
		"from empty":             {"", "2026-09-27", "from_day", "bad_day"},
		"to not a calendar day":  {"2026-02-01", "2026-02-30", "to_day", "bad_day"},
		"to a timestamp":         {"2026-09-01", "2026-09-27T00:00:00Z", "to_day", "bad_day"},
		"from after to":          {"2026-09-27", "2026-09-26", "to_day", "range_reversed"},
		"367 days":               {"2026-01-01", "2027-01-02", "to_day", "range_too_long"},
		"centuries (saturating)": {"0001-01-01", "9999-12-31", "to_day", "range_too_long"},
	} {
		t.Run(name, func(t *testing.T) {
			s := &Server{repo: mocks.NewMockRepository(t)}
			_, err := s.GetAiSpendReport(context.Background(), &pb_admin.GetAiSpendReportRequest{FromDay: c.from, ToDay: c.to})
			fv := fieldOf(t, err)
			require.Equal(t, c.field, fv.Field)
			require.Contains(t, fv.Description, c.reason)
		})
	}

	// The bound is inclusive: 366 days counting both ends pass (a leap year, and 2026-01-01 through
	// 2027-01-01), and so does a single day.
	for _, p := range [][2]string{{"2028-01-01", "2028-12-31"}, {"2026-01-01", "2027-01-01"}, {"2026-09-27", "2026-09-27"}} {
		s := aiSpendServer(t, p[0], p[1], &entity.AISpendReport{FromDay: p[0], ToDay: p[1]}, nil)
		resp, err := s.GetAiSpendReport(context.Background(), &pb_admin.GetAiSpendReportRequest{FromDay: p[0], ToDay: p[1]})
		require.NoError(t, err, "%v", p)
		require.Equal(t, p[0], resp.FromDay)
		require.Equal(t, p[1], resp.ToDay)
	}
}

// TestGetAiSpendReportMapsUnknownAsAbsent — the ABSENCE rule of P-01: a Decimal left unset is unknown,
// never zero; a real zero is sent as "0".
//
// MUTATIONS IT CATCHES: our_usd / their_usd / usd / total_usd emitted from .Decimal regardless of
// Valid (unknown arrives as "0"); a zero-call line filtered out (the provider that only billed us
// disappears); actor_admin_id not carried; the provider order of the store reshuffled; their_bucket_tz
// not carried, or filled for a line with no number of theirs (D-17).
func TestGetAiSpendReportMapsUnknownAsAbsent(t *testing.T) {
	adminID := 7
	rep := &entity.AISpendReport{
		FromDay: "2026-09-01", ToDay: "2026-09-27", Timezone: "Europe/Warsaw",
		TotalUSD: spendUSD("1.253000"), Calls: 9, Failed: 4, Unpriced: 2,
		ByProvider: []entity.AISpendByProvider{
			// present only through ai_provider_cost_daily: their number, ours unknown, no calls
			{ProviderKey: "openai", TheirUSD: spendUSD("12.500000"), TheirBucketTZ: "UTC"},
			// priced sum beside their number
			{ProviderKey: "openrouter", OurUSD: spendUSD("1.253000"), TheirUSD: spendUSD("1.2"), TheirBucketTZ: "UTC", Calls: 4, Failed: 1},
			// only unpriced calls: ours unknown, unpriced counted
			{ProviderKey: "fal", Calls: 2, Unpriced: 2},
			// only free calls: a real zero
			{ProviderKey: "meshy", OurUSD: spendUSD("0.000000"), Calls: 3, Failed: 3},
		},
		ByActor: []entity.AISpendByActor{
			{Actor: "jeka", ActorAdminID: &adminID, Purpose: "chat.note_markdown", ProviderKey: "openrouter",
				Model: "anthropic/claude-sonnet-5", USD: spendUSD("0.000312"), Calls: 4},
			{Actor: "system", Purpose: "threed", ProviderKey: "fal", Model: "fal-ai/trellis", Calls: 2},
		},
	}
	s := aiSpendServer(t, "2026-09-01", "2026-09-27", rep, nil)

	resp, err := s.GetAiSpendReport(context.Background(),
		&pb_admin.GetAiSpendReportRequest{FromDay: "2026-09-01", ToDay: "2026-09-27"})
	require.NoError(t, err)

	require.Equal(t, "2026-09-01", resp.FromDay)
	require.Equal(t, "2026-09-27", resp.ToDay)
	require.Equal(t, "Europe/Warsaw", resp.Timezone)
	require.Equal(t, "1.253", resp.GetTotalUsd().GetValue())
	require.Equal(t, []int32{9, 4, 2}, []int32{resp.Calls, resp.Failed, resp.Unpriced})

	require.Len(t, resp.ByProvider, 4)
	var order []string
	for _, p := range resp.ByProvider {
		order = append(order, p.ProviderKey)
	}
	require.Equal(t, []string{"openai", "openrouter", "fal", "meshy"}, order, "the store's order is the panel's")

	openai := resp.ByProvider[0]
	require.Nil(t, openai.OurUsd, "a provider with no ledger row has no number of ours: absent, never 0")
	require.Equal(t, "12.5", openai.GetTheirUsd().GetValue())
	require.Equal(t, "UTC", openai.GetTheirBucketTz(), "their days are labelled with their zone")
	require.Zero(t, openai.Calls)

	openrouter := resp.ByProvider[1]
	require.Equal(t, "1.253", openrouter.GetOurUsd().GetValue())
	require.Equal(t, "1.2", openrouter.GetTheirUsd().GetValue())
	require.Equal(t, int32(4), openrouter.Calls)
	require.Equal(t, int32(1), openrouter.Failed)

	fal := resp.ByProvider[2]
	require.Nil(t, fal.OurUsd, "only unpriced calls: our number is unknown")
	require.Nil(t, fal.TheirUsd, "no cost API rows: their number is unknown")
	require.Empty(t, fal.GetTheirBucketTz(), "no number of theirs, no zone of theirs")
	require.Equal(t, int32(2), fal.Unpriced)

	meshy := resp.ByProvider[3]
	require.NotNil(t, meshy.OurUsd, "only free calls: a real zero travels")
	require.Equal(t, "0", meshy.OurUsd.Value)

	require.Len(t, resp.ByActor, 2)
	jeka := resp.ByActor[0]
	require.Equal(t, "jeka", jeka.Actor)
	require.Equal(t, int32(7), jeka.ActorAdminId)
	require.Equal(t, "chat.note_markdown", jeka.Purpose)
	require.Equal(t, "openrouter", jeka.ProviderKey)
	require.Equal(t, "anthropic/claude-sonnet-5", jeka.Model)
	require.Equal(t, "0.000312", jeka.GetUsd().GetValue(), "sub-cent spend is not rounded to a zero")
	require.Equal(t, int32(4), jeka.Calls)
	system := resp.ByActor[1]
	require.Equal(t, "system", system.Actor)
	require.Zero(t, system.ActorAdminId, "unresolved actor → 0")
	require.Nil(t, system.Usd)
	require.Equal(t, int32(2), system.Calls)
}

// TestGetAiSpendReportTotalsZeroVersusUnknown.
//
// MUTATIONS IT CATCHES: total_usd emitted only when non-zero (a free-only period reads as unknown);
// total_usd emitted from .Decimal regardless of Valid (an unpriced period reads as $0).
func TestGetAiSpendReportTotalsZeroVersusUnknown(t *testing.T) {
	t.Run("a period whose only calls were free: total 0", func(t *testing.T) {
		s := aiSpendServer(t, "2026-09-01", "2026-09-07", &entity.AISpendReport{
			FromDay: "2026-09-01", ToDay: "2026-09-07", TotalUSD: spendUSD("0.000000"), Calls: 2, Failed: 2,
			ByProvider: []entity.AISpendByProvider{{ProviderKey: "openrouter", OurUSD: spendUSD("0.000000"), Calls: 2, Failed: 2}},
		}, nil)
		resp, err := s.GetAiSpendReport(context.Background(),
			&pb_admin.GetAiSpendReportRequest{FromDay: "2026-09-01", ToDay: "2026-09-07"})
		require.NoError(t, err)
		require.NotNil(t, resp.TotalUsd)
		require.Equal(t, "0", resp.TotalUsd.Value)
	})
	t.Run("a period with only unpriced calls: total absent", func(t *testing.T) {
		s := aiSpendServer(t, "2026-09-01", "2026-09-07", &entity.AISpendReport{
			FromDay: "2026-09-01", ToDay: "2026-09-07", Calls: 3, Unpriced: 3,
			ByProvider: []entity.AISpendByProvider{{ProviderKey: "fal", Calls: 3, Unpriced: 3}},
		}, nil)
		resp, err := s.GetAiSpendReport(context.Background(),
			&pb_admin.GetAiSpendReportRequest{FromDay: "2026-09-01", ToDay: "2026-09-07"})
		require.NoError(t, err)
		require.Nil(t, resp.TotalUsd)
		require.Equal(t, int32(3), resp.Unpriced)
	})
	t.Run("an empty period: nothing known, empty lists", func(t *testing.T) {
		s := aiSpendServer(t, "2026-09-01", "2026-09-07", &entity.AISpendReport{FromDay: "2026-09-01", ToDay: "2026-09-07"}, nil)
		resp, err := s.GetAiSpendReport(context.Background(),
			&pb_admin.GetAiSpendReportRequest{FromDay: "2026-09-01", ToDay: "2026-09-07"})
		require.NoError(t, err)
		require.Nil(t, resp.TotalUsd)
		require.Empty(t, resp.ByProvider)
		require.Empty(t, resp.ByActor)
	})
}

// TestGetAiSpendReportStoreErrors.
//
// MUTATION IT CATCHES: a store failure answered as success or with its raw text; a store field
// violation turned into Internal.
func TestGetAiSpendReportStoreErrors(t *testing.T) {
	s := aiSpendServer(t, "2026-09-01", "2026-09-07", nil, errors.New("dial tcp 10.0.0.1:3306: i/o timeout"))
	_, err := s.GetAiSpendReport(context.Background(), &pb_admin.GetAiSpendReportRequest{FromDay: "2026-09-01", ToDay: "2026-09-07"})
	require.Equal(t, codes.Internal, status.Code(err))
	require.NotContains(t, status.Convert(err).Message(), "10.0.0.1")

	s = aiSpendServer(t, "2026-09-01", "2026-09-07", nil,
		entity.NewFieldViolation("to_day", "range_reversed", "", "the last day must not precede the first"))
	_, err = s.GetAiSpendReport(context.Background(), &pb_admin.GetAiSpendReportRequest{FromDay: "2026-09-01", ToDay: "2026-09-07"})
	require.Equal(t, "to_day", fieldOf(t, err).Field)
}
