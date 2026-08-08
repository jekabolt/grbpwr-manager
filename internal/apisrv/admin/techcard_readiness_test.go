package admin

import (
	"context"
	"database/sql"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// readinessByKey indexes a checklist by its machine key so a case asserts only the rows it cares
// about (and, via the length check, that no row was silently added or dropped).
func readinessByKey(rows []*pb_admin.TechCardReadinessRequirement) map[string]*pb_admin.TechCardReadinessRequirement {
	m := make(map[string]*pb_admin.TechCardReadinessRequirement, len(rows))
	for _, r := range rows {
		m[r.Key] = r
	}
	return m
}

func readinessCardForFacts(f entity.TechCardReadinessFacts, staleSection entity.TechCardSignoffSection) *entity.TechCard {
	card := &entity.TechCard{}
	sections := []entity.TechCardSignoffSection{
		entity.SignoffDesign, entity.SignoffConstruction, entity.SignoffMaterials,
		entity.SignoffColour, entity.SignoffLabels, entity.SignoffPackaging, entity.SignoffCosting,
	}
	current := dto.TechCardSectionDigests(&card.TechCardInsert)
	for i := 0; i < f.Signoffs && i < len(sections); i++ {
		signoff := entity.TechCardSignoff{Section: sections[i], State: entity.SignoffStatePending}
		if i < f.SignoffsApproved {
			signoff.State = entity.SignoffStateApproved
			digest := current[signoff.Section]
			if signoff.Section == staleSection {
				digest = "digest-of-older-content"
			}
			signoff.SignedDigest = sql.NullString{String: digest, Valid: true}
		}
		card.Signoffs = append(card.Signoffs, signoff)
	}
	return card
}

// TestGetTechCardReadiness drives the same facts→checklist function the RPC uses, one row per
// interesting state. Each case names the stage checklist it exercises plus the release list, which
// is computed for every card regardless of stage.
func TestGetTechCardReadiness(t *testing.T) {
	tests := []struct {
		name string
		// facts is what the store would have counted.
		facts entity.TechCardReadinessFacts

		wantNext        pb_common.TechCardStage
		wantNextKeys    []string          // exact keys, in display order
		wantNextMet     map[string]bool   // key -> met
		wantNextDetail  map[string]string // key -> exact detail sentence (unmet rows only)
		wantNextUnknown map[string]bool   // key -> the server could not answer (Р4)
		wantNextReady   bool
		wantReleaseMet  map[string]bool
		wantReleaseDetl map[string]string
		wantReleaseOK   bool
		staleSection    entity.TechCardSignoffSection
	}{
		{
			name:         "idea to proto: an empty card fails every entry condition",
			facts:        entity.TechCardReadinessFacts{Stage: entity.TechCardStageIdea},
			wantNext:     pb_common.TechCardStage_TECH_CARD_STAGE_PROTO,
			wantNextKeys: []string{"style_number", "bom_fabric", "first_sample"},
			wantNextMet:  map[string]bool{"style_number": false, "bom_fabric": false, "first_sample": false},
			wantNextDetail: map[string]string{
				"style_number": "no style number set",
				"bom_fabric":   "no fabric line in the BOM",
				"first_sample": "no sample recorded",
			},
			wantNextReady: false,
			wantReleaseMet: map[string]bool{
				"style_number": false, "size_range": false, "bom_fabric": false, "costing": false,
				"colorway_linked": false,
				// no colourway at all -> vacuously met; colorway_linked already carries the failure
				"lab_dip": true, "signoffs": false,
			},
			wantReleaseDetl: map[string]string{
				"costing":  "no costing recorded",
				"signoffs": "no sign-off recorded",
			},
			wantReleaseOK: false,
		},
		{
			name: "idea to proto: a style number, a fabric line and one sample flip it green",
			facts: entity.TechCardReadinessFacts{
				Stage: entity.TechCardStageIdea, HasStyleNumber: true, BomFabricLines: 1, Samples: 1,
			},
			wantNext:      pb_common.TechCardStage_TECH_CARD_STAGE_PROTO,
			wantNextKeys:  []string{"style_number", "bom_fabric", "first_sample"},
			wantNextMet:   map[string]bool{"style_number": true, "bom_fabric": true, "first_sample": true},
			wantNextReady: true,
			// the release list is unaffected by the stage move
			wantReleaseMet: map[string]bool{"style_number": true, "size_range": false, "bom_fabric": true},
			wantReleaseOK:  false,
		},
		{
			name: "proto to fit: a scrapped-only card has no proto sample",
			facts: entity.TechCardReadinessFacts{
				Stage: entity.TechCardStageProto, Fittings: 0, ProtoSamples: 0,
			},
			wantNext:     pb_common.TechCardStage_TECH_CARD_STAGE_FIT,
			wantNextKeys: []string{"fitting_recorded", "first_sample"},
			wantNextMet:  map[string]bool{"fitting_recorded": false, "first_sample": false},
			wantNextDetail: map[string]string{
				"fitting_recorded": "no fitting recorded",
				"first_sample":     "no proto sample recorded",
			},
			wantNextReady:  false,
			wantReleaseMet: map[string]bool{"lab_dip": true},
			wantReleaseOK:  false,
		},
		{
			name: "fit to sms: an approved fitting is not enough while a change request is open",
			facts: entity.TechCardReadinessFacts{
				Stage: entity.TechCardStageFit, FittingsApproved: 1, OpenChangeRequests: 1,
			},
			wantNext:       pb_common.TechCardStage_TECH_CARD_STAGE_SMS,
			wantNextKeys:   []string{"fit_approved", "fittings_resolved"},
			wantNextMet:    map[string]bool{"fit_approved": true, "fittings_resolved": false},
			wantNextDetail: map[string]string{"fittings_resolved": "1 fitting change request is still open"},
			wantNextReady:  false,
			wantReleaseOK:  false,
		},
		{
			name: "sms to pp: a partly linked BOM and no colourway",
			facts: entity.TechCardReadinessFacts{
				Stage: entity.TechCardStageSMS, SmsSamples: 0, LiveColorways: 0,
				BomLines: 5, BomLinkedLines: 3,
			},
			wantNext:     pb_common.TechCardStage_TECH_CARD_STAGE_PP,
			wantNextKeys: []string{"sms_sample", "colorway_linked", "bom_linked"},
			wantNextMet:  map[string]bool{"sms_sample": false, "colorway_linked": false, "bom_linked": false},
			wantNextDetail: map[string]string{
				"sms_sample":      "no sms sample recorded",
				"colorway_linked": "no live colourway",
				"bom_linked":      "2 of 5 BOM slots have no article (no default and not pinned by every live colourway)",
			},
			wantNextReady: false,
			wantReleaseOK: false,
		},
		{
			name: "sms to pp: an empty BOM is not vacuously linked",
			facts: entity.TechCardReadinessFacts{
				Stage: entity.TechCardStageSMS, SmsSamples: 1, LiveColorways: 1, BomLines: 0, BomLinkedLines: 0,
			},
			wantNext:       pb_common.TechCardStage_TECH_CARD_STAGE_PP,
			wantNextKeys:   []string{"sms_sample", "colorway_linked", "bom_linked"},
			wantNextMet:    map[string]bool{"sms_sample": true, "colorway_linked": true, "bom_linked": false},
			wantNextDetail: map[string]string{"bom_linked": "the BOM is empty"},
			wantNextReady:  false,
			wantReleaseOK:  false,
		},
		{
			name: "sms to pp: a fully linked BOM with a sample and a colourway is ready",
			facts: entity.TechCardReadinessFacts{
				Stage: entity.TechCardStageSMS, SmsSamples: 1, LiveColorways: 2, LabDipPendingColorways: 2,
				BomLines: 5, BomLinkedLines: 5,
			},
			wantNext:      pb_common.TechCardStage_TECH_CARD_STAGE_PP,
			wantNextKeys:  []string{"sms_sample", "colorway_linked", "bom_linked"},
			wantNextMet:   map[string]bool{"sms_sample": true, "colorway_linked": true, "bom_linked": true},
			wantNextReady: true,
			// two colourways, neither lab-dipped
			wantReleaseMet:  map[string]bool{"colorway_linked": true, "lab_dip": false},
			wantReleaseDetl: map[string]string{"lab_dip": "2 of 2 colourways have no approved lab dip"},
			wantReleaseOK:   false,
		},
		{
			// Р4. The card's DISTINCT pattern size_id count used to answer this row, and it lied in
			// both directions — the client files every sheet under the smallest size of the range as a
			// storage artefact. It now reads the Ф6.3 index, and this fixture has none: the honest
			// answer is UNKNOWN. Load-bearing assertion: wantNextReady is TRUE. An UNKNOWN row must not
			// hold a stage back, or shipping this would have blocked the whole portfolio at once.
			name: "pp to prod: with no size index the patterns row says UNKNOWN and blocks nothing",
			facts: entity.TechCardReadinessFacts{
				Stage: entity.TechCardStagePP, PpSamples: 1, ProductionRuns: 1, Sizes: 5,
			},
			wantNext:        pb_common.TechCardStage_TECH_CARD_STAGE_PROD,
			wantNextKeys:    []string{"pp_sample", "run_planned", "patterns"},
			wantNextMet:     map[string]bool{"pp_sample": true, "run_planned": true, "patterns": false},
			wantNextUnknown: map[string]bool{"pp_sample": false, "run_planned": false, "patterns": true},
			wantNextReady:   true,
			wantReleaseOK:   false,
		},
		{
			name: "prod: the last stage has no next stage and no entry conditions",
			facts: entity.TechCardReadinessFacts{
				Stage: entity.TechCardStageProd, HasStyleNumber: true, Sizes: 3, BomFabricLines: 1,
				HasCosting: true, HasCostingCurrency: true, LiveColorways: 2, LabDipPendingColorways: 0,
				Signoffs: 4, SignoffsApproved: 4,
			},
			wantNext:      pb_common.TechCardStage_TECH_CARD_STAGE_UNKNOWN,
			wantNextKeys:  []string{},
			wantNextReady: true, // vacuous: a client gates the "advance" affordance on next_stage
			wantReleaseMet: map[string]bool{
				"style_number": true, "size_range": true, "bom_fabric": true, "costing": true,
				"colorway_linked": true, "lab_dip": true, "signoffs": true,
			},
			wantReleaseOK: true,
		},
		{
			name: "release: a costing row without a currency, and a pending sign-off",
			facts: entity.TechCardReadinessFacts{
				Stage: entity.TechCardStageProd, HasStyleNumber: true, Sizes: 3, BomFabricLines: 1,
				HasCosting: true, HasCostingCurrency: false, LiveColorways: 3, LabDipPendingColorways: 1,
				Signoffs: 4, SignoffsApproved: 3,
			},
			wantNext:      pb_common.TechCardStage_TECH_CARD_STAGE_UNKNOWN,
			wantNextKeys:  []string{},
			wantNextReady: true,
			wantReleaseMet: map[string]bool{
				"costing": false, "lab_dip": false, "signoffs": false, "colorway_linked": true,
			},
			wantReleaseDetl: map[string]string{
				"costing":  "the costing has no currency",
				"lab_dip":  "1 of 3 colourways have no approved lab dip",
				"signoffs": "1 of 4 sign-offs are not approved",
			},
			wantReleaseOK: false,
		},
		{
			name: "release: an approved construction sign-off with an old digest is stale",
			facts: entity.TechCardReadinessFacts{
				Stage: entity.TechCardStageProd, HasStyleNumber: true, Sizes: 3, BomFabricLines: 1,
				HasCosting: true, HasCostingCurrency: true, LiveColorways: 1,
				Signoffs: 2, SignoffsApproved: 2,
			},
			staleSection:   entity.SignoffConstruction,
			wantNext:       pb_common.TechCardStage_TECH_CARD_STAGE_UNKNOWN,
			wantNextKeys:   []string{},
			wantNextReady:  true,
			wantReleaseMet: map[string]bool{"signoffs": false},
			wantReleaseDetl: map[string]string{
				"signoffs": "construction approval is stale",
			},
			wantReleaseOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := mocks.NewMockRepository(t)
			tc := mocks.NewMockTechCards(t)
			repo.EXPECT().TechCards().Return(tc)
			tc.EXPECT().GetTechCardPatternSizeIndex(mock.Anything, 7).
				Return(map[string]entity.PatternSizeIndexRow{}, nil).Maybe()
			tc.EXPECT().GetTechCardReadinessSnapshot(mock.Anything, 7).
				Return(tt.facts, readinessCardForFacts(tt.facts, tt.staleSection), nil)
			// Р4: the `patterns` row now reads the Ф6.3 size index. These fixtures carry none, which
			// is the state of every card on the day this ships — and the assertions below say what
			// that has to produce: UNKNOWN, and a stage that is NOT held back by it.
			tc.EXPECT().GetTechCardPatternSizeIndex(mock.Anything, 7).
				Return(map[string]entity.PatternSizeIndexRow{}, nil).Maybe()

			s := &Server{repo: repo}
			resp, err := s.GetTechCardReadiness(context.Background(), &pb_admin.GetTechCardReadinessRequest{TechCardId: 7})
			require.NoError(t, err)

			require.Equal(t, tt.wantNext, resp.NextStage)
			require.Equal(t, tt.wantNextReady, resp.NextStageReady)
			require.Equal(t, tt.wantReleaseOK, resp.ReleaseReady)

			gotKeys := make([]string, 0, len(resp.NextStageRequirements))
			for _, r := range resp.NextStageRequirements {
				gotKeys = append(gotKeys, r.Key)
			}
			require.Equal(t, tt.wantNextKeys, gotKeys, "next-stage checklist keys and order")

			next := readinessByKey(resp.NextStageRequirements)
			release := readinessByKey(resp.ReleaseRequirements)
			assertReadiness(t, "next_stage", next, tt.wantNextMet, tt.wantNextDetail)
			for key, want := range tt.wantNextUnknown {
				row, ok := next[key]
				require.Truef(t, ok, "next_stage checklist is missing key %q", key)
				require.Equalf(t, want, row.Unknown, "next_stage row %q unknown flag", key)
				if want {
					require.NotEmptyf(t, row.Detail, "an UNKNOWN row must say WHY there is no verdict (%q)", key)
					require.Falsef(t, row.Met, "an UNKNOWN row is never met (%q)", key)
				}
			}
			assertReadiness(t, "release", release, tt.wantReleaseMet, tt.wantReleaseDetl)

			// A met row never explains itself: detail is the failure reason, nothing else.
			for _, r := range append(resp.NextStageRequirements, resp.ReleaseRequirements...) {
				require.NotEmpty(t, r.Label, "row %q must carry a label", r.Key)
				if r.Met {
					require.Empty(t, r.Detail, "met row %q must have no detail", r.Key)
				} else {
					require.NotEmpty(t, r.Detail, "unmet row %q must explain itself", r.Key)
				}
			}
		})
	}
}

func assertReadiness(t *testing.T, list string, got map[string]*pb_admin.TechCardReadinessRequirement,
	wantMet map[string]bool, wantDetail map[string]string) {
	t.Helper()
	for key, met := range wantMet {
		row, ok := got[key]
		require.Truef(t, ok, "%s checklist is missing key %q", list, key)
		require.Equalf(t, met, row.Met, "%s checklist row %q", list, key)
	}
	for key, detail := range wantDetail {
		row, ok := got[key]
		require.Truef(t, ok, "%s checklist is missing key %q", list, key)
		require.Equalf(t, detail, row.Detail, "%s checklist detail for %q", list, key)
	}
}

// TestGetTechCardReadinessReleaseListIsStable: the release checklist is the same seven conditions
// for every card, whatever its stage — a client can render it as a fixed table.
func TestGetTechCardReadinessReleaseListIsStable(t *testing.T) {
	for _, stage := range []entity.TechCardStage{
		entity.TechCardStageIdea, entity.TechCardStageProto, entity.TechCardStageFit,
		entity.TechCardStageSMS, entity.TechCardStagePP, entity.TechCardStageProd,
	} {
		repo := mocks.NewMockRepository(t)
		tc := mocks.NewMockTechCards(t)
		repo.EXPECT().TechCards().Return(tc)
		facts := entity.TechCardReadinessFacts{Stage: stage}
		tc.EXPECT().GetTechCardPatternSizeIndex(mock.Anything, 3).
			Return(map[string]entity.PatternSizeIndexRow{}, nil).Maybe()
		tc.EXPECT().GetTechCardReadinessSnapshot(mock.Anything, 3).
			Return(facts, readinessCardForFacts(facts, ""), nil)

		s := &Server{repo: repo}
		resp, err := s.GetTechCardReadiness(context.Background(), &pb_admin.GetTechCardReadinessRequest{TechCardId: 3})
		require.NoError(t, err)

		keys := make([]string, 0, len(resp.ReleaseRequirements))
		for _, r := range resp.ReleaseRequirements {
			keys = append(keys, r.Key)
		}
		require.Equal(t, []string{
			"style_number", "size_range", "bom_fabric", "costing", "colorway_linked", "lab_dip", "signoffs",
		}, keys, "release checklist for stage %s", stage)
	}
}

func TestGetTechCardReadinessNotFound(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardReadinessSnapshot(mock.Anything, 7).
		Return(entity.TechCardReadinessFacts{}, nil, sql.ErrNoRows)

	s := &Server{repo: repo}
	_, err := s.GetTechCardReadiness(context.Background(), &pb_admin.GetTechCardReadinessRequest{TechCardId: 7})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestGetTechCardReadinessBadRequest(t *testing.T) {
	s := &Server{repo: mocks.NewMockRepository(t)}
	_, err := s.GetTechCardReadiness(context.Background(), &pb_admin.GetTechCardReadinessRequest{TechCardId: 0})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
