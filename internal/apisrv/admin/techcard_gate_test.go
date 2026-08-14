package admin

import (
	"database/sql"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The capability gate (§8) and the two sign-off belts that stand behind it (§8 tail, §9).
//
// Every «passes» case here proves itself the same way: the store mock is reached and answers with a
// conflict, so an Aborted means the request walked the whole gauntlet. An unmet mock expectation
// fails the test on its own, which is what makes «reached the store» a real assertion rather than a
// hopeful reading of an error code.

const (
	gateFusingKeyA = "01J0PRESSKEY0000000000001A"
	gateFusingKeyB = "01J0PRESSKEY0000000000002B"
)

func gateZone() pb_common.TechCardGarmentZone {
	return pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_OUTER
}

func gateDecimal(v string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
}

// storedCardWithMachineFacts is the card the frozen bundle must not be allowed to flatten: one step
// that says what it is sewn on. Migration 0306 produced exactly this shape out of every legacy
// `lockstitch` row, so it is the common case on beta, not a contrived one.
func storedCardWithMachineFacts() *entity.TechCard {
	return &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		Operations: []entity.TechCardOperation{{
			OperationNumber: sql.NullInt32{Int32: 10, Valid: true},
			OperationType:   entity.OpTypeMachine,
			Zone:            "outer",
			MachineType:     sql.NullString{String: "overlock", Valid: true},
		}},
	}}
}

func gateUpdateReq(tc *pb_common.TechCardInsert) *pb_admin.UpdateTechCardRequest {
	return &pb_admin.UpdateTechCardRequest{Id: 7, ExpectedLockVersion: 3, TechCard: tc}
}

func gateInsert(aware bool) *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{
		StyleNumber:        "TC-GATE",
		Name:               "gate",
		MachineFieldsAware: aware,
	}
}

// gateStage is HOW FAR the request is expected to get, and the mock is built to match it. It is an
// assertion in its own right, not bookkeeping: rule 1 reads the payload and owes storage nothing, so
// a request it refuses must never have loaded the card — expecting that load would quietly permit
// the ordering this gate exists to guarantee (rule 1 ahead of the dto conversion, which happens
// after the load).
type gateStage int

const (
	gateStopsAtWire   gateStage = iota // refused off the payload alone; nothing is read
	gateStopsAtStored                  // the stored card is read, the write is never attempted
	gateReachesStore                   // every gate passed and the write was attempted
)

// gateUpdate runs UpdateTechCard against a stored card, with the store answering a conflict so a
// request that survives every gate is distinguishable from one that was refused.
func gateUpdate(t *testing.T, stored *entity.TechCard, tc *pb_common.TechCardInsert, stage gateStage) error {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	if stage > gateStopsAtWire {
		repo.EXPECT().TechCards().Return(techCards)
		techCards.EXPECT().GetTechCardByIdConsistent(mock.Anything, 7).Return(stored, nil)
	}
	if stage == gateReachesStore {
		techCards.EXPECT().UpdateTechCardAndListOrphanedPatternURLs(mock.Anything, 7,
			mock.AnythingOfType("*entity.TechCardInsert"), 3).Return(nil, entity.ErrTechCardConflict)
	}
	_, err := (&Server{repo: repo}).UpdateTechCard(fullAccessCtx(), gateUpdateReq(tc))
	return err
}

// --- §8 rule 2: the stored card holds facts the payload cannot carry ------------------------------

func TestUpdateTechCardRefusesOutdatedClientOverStoredMachineFacts(t *testing.T) {
	tests := []struct {
		name   string
		stored *entity.TechCard
	}{
		{"a step names its machine", storedCardWithMachineFacts()},
		{"the card holds an equipment profile", &entity.TechCard{TechCardInsert: entity.TechCardInsert{
			Construction: &entity.TechCardConstruction{
				EquipmentDefaults: &entity.TechCardEquipmentDefaults{
					Presses: []entity.TechCardPressProfile{{
						ProfileKey: gateFusingKeyA, PressEquipment: "fusing_press",
					}},
				},
			},
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := gateUpdate(t, tt.stored, gateInsert(false), gateStopsAtStored)
			require.Equal(t, codes.FailedPrecondition, status.Code(err))
			require.Contains(t, status.Convert(err).Message(), "outdated admin client")
			require.Contains(t, status.Convert(err).Message(), "update the admin panel")
		})
	}
}

// --- §8 rule 1: the payload echoes fields the sender does not declare -----------------------------

func TestUpdateTechCardRefusesOutdatedClientEchoingMachineFields(t *testing.T) {
	tests := []struct {
		name    string
		payload func() *pb_common.TechCardInsert
	}{
		{"a step type the split added", func() *pb_common.TechCardInsert {
			tc := gateInsert(false)
			tc.Operations = []*pb_common.TechCardOperation{{
				OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
				Zone:          gateZone(),
			}}
			return tc
		}},
		{"a ВТО value on a step the old bundle could always send", func() *pb_common.TechCardInsert {
			tc := gateInsert(false)
			tc.Operations = []*pb_common.TechCardOperation{{
				OperationType:     pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING,
				Zone:              gateZone(),
				PressTemperatureC: 150,
			}}
			return tc
		}},
		{"an explicit «без пара», which is a stated fact and not an absence", func() *pb_common.TechCardInsert {
			tc := gateInsert(false)
			steam := false
			tc.Operations = []*pb_common.TechCardOperation{{
				OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING,
				Zone:          gateZone(),
				PressSteam:    &steam,
			}}
			return tc
		}},
		{"the equipment wrapper itself", func() *pb_common.TechCardInsert {
			tc := gateInsert(false)
			tc.Construction = &pb_common.TechCardConstruction{
				EquipmentDefaults: &pb_common.TechCardEquipmentDefaults{},
			}
			return tc
		}},
		// THE ECHO THE CONVERTER HAS AN OPINION ABOUT. A machine value on a step type that cannot
		// carry one is refused by parseOperationEquipment in words — «the machine settings belong to a
		// machine step, clear the field» — and that sentence is useless to the sender, whose form has
		// no such field to clear: it is echoing a value it never rendered. The gate has the sentence
		// that can be acted on, so the gate has to speak first, which it only does while rule 1 sits
		// ahead of the conversion.
		{"a machine value echoed onto a step type that cannot carry one", func() *pb_common.TechCardInsert {
			tc := gateInsert(false)
			tc.Operations = []*pb_common.TechCardOperation{{
				OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_HANDWORK,
				Zone:          gateZone(),
				ThreadCount:   4,
			}}
			return tc
		}},
		{"a ВТО value echoed onto a step type that cannot carry one", func() *pb_common.TechCardInsert {
			tc := gateInsert(false)
			tc.Operations = []*pb_common.TechCardOperation{{
				OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_HANDWORK,
				Zone:          gateZone(),
				PressCloth:    pb_common.TechCardPressCloth_TECH_CARD_PRESS_CLOTH_TEFLON_SHEET,
			}}
			return tc
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// gateStopsAtWire is the load-bearing half: the refusal must come off the payload, before
			// the card is read and therefore before the conversion that reads it.
			err := gateUpdate(t, &entity.TechCard{}, tt.payload(), gateStopsAtWire)
			require.Equal(t, codes.FailedPrecondition, status.Code(err))
			require.Contains(t, status.Convert(err).Message(), "does not declare support")
		})
	}
}

func TestCreateTechCardRefusesOutdatedClientEchoingMachineFields(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := gateInsert(false)
	tc.Operations = []*pb_common.TechCardOperation{{
		OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS_OPEN,
		Zone:          gateZone(),
	}}
	_, err := (&Server{repo: repo}).CreateTechCard(fullAccessCtx(), &pb_admin.CreateTechCardRequest{TechCard: tc})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "outdated admin client")
}

// --- §8: what the gate must NOT break -------------------------------------------------------------

// A stale bundle saving a legacy step onto a card with nothing to lose is the case the gate exists to
// leave alone. The legacy token canonicalises into (machine, lockstitch) INSIDE the converter, which
// is precisely why rule 1 reads the wire: an entity-side check would see «speaks MACHINE» here and
// lock out every unupdated admin in the building.
func TestUpdateTechCardAllowsOutdatedClientOnCardWithoutMachineFacts(t *testing.T) {
	tc := gateInsert(false)
	tc.Operations = []*pb_common.TechCardOperation{{
		OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_LOCKSTITCH,
		Zone:          gateZone(),
	}}
	stored := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		Operations: []entity.TechCardOperation{{
			OperationNumber: sql.NullInt32{Int32: 10, Valid: true},
			OperationType:   entity.OpTypeFusing,
			Zone:            "outer",
		}},
	}}
	err := gateUpdate(t, stored, tc, gateReachesStore)
	require.Equal(t, codes.Aborted, status.Code(err), "a legacy save with nothing to erase must go through")
}

func TestUpdateTechCardAllowsAwareClientOverStoredMachineFacts(t *testing.T) {
	tc := gateInsert(true)
	tc.Operations = []*pb_common.TechCardOperation{{
		OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
		Zone:          gateZone(),
		MachineType:   pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK,
	}}
	err := gateUpdate(t, storedCardWithMachineFacts(), tc, gateReachesStore)
	require.Equal(t, codes.Aborted, status.Code(err))
}

// storedHasMachineFacts is rule 2's whole surface, so it is checked column by column rather than
// through one representative case: a column forgotten here is a column the stale bundle silently
// blanks, and nothing else in the system would notice.
func TestStoredHasMachineFactsCoversEveryNewColumn(t *testing.T) {
	set := []struct {
		name string
		fill func(*entity.TechCardOperation)
	}{
		{"machine_type", func(o *entity.TechCardOperation) { o.MachineType = sql.NullString{String: "overlock", Valid: true} }},
		{"machine_profile_key", func(o *entity.TechCardOperation) {
			o.MachineProfileKey = sql.NullString{String: gateFusingKeyA, Valid: true}
		}},
		{"thread_count", func(o *entity.TechCardOperation) { o.ThreadCount = sql.NullInt32{Int32: 5, Valid: true} }},
		{"needle_type", func(o *entity.TechCardOperation) { o.NeedleType = sql.NullString{String: "ballpoint", Valid: true} }},
		{"needle_size_nm", func(o *entity.TechCardOperation) { o.NeedleSizeNm = sql.NullInt32{Int32: 90, Valid: true} }},
		{"thread_tension", func(o *entity.TechCardOperation) { o.ThreadTension = sql.NullString{String: "looser", Valid: true} }},
		{"thread_tension_note", func(o *entity.TechCardOperation) {
			o.ThreadTensionNote = sql.NullString{String: "на пол-оборота", Valid: true}
		}},
		{"stitch_width_mm", func(o *entity.TechCardOperation) { o.StitchWidthMm = gateDecimal("5.5") }},
		{"press_equipment", func(o *entity.TechCardOperation) {
			o.PressEquipment = sql.NullString{String: "fusing_press", Valid: true}
		}},
		{"press_profile_key", func(o *entity.TechCardOperation) {
			o.PressProfileKey = sql.NullString{String: gateFusingKeyA, Valid: true}
		}},
		{"press_temperature_c", func(o *entity.TechCardOperation) { o.PressTemperatureC = sql.NullInt32{Int32: 150, Valid: true} }},
		{"press_dwell_sec", func(o *entity.TechCardOperation) { o.PressDwellSec = sql.NullInt32{Int32: 12, Valid: true} }},
		{"press_pressure_n_cm2", func(o *entity.TechCardOperation) { o.PressPressureNCm2 = gateDecimal("3.5") }},
		// «Без пара» is an instruction to the floor, and a stale save would turn it back into «as it
		// comes». NULL, one line below, is the absence it must stay distinguishable from.
		{"press_steam = false", func(o *entity.TechCardOperation) { o.PressSteam = sql.NullBool{Bool: false, Valid: true} }},
		{"press_cloth", func(o *entity.TechCardOperation) { o.PressCloth = sql.NullString{String: "none", Valid: true} }},
	}
	for _, tt := range set {
		t.Run(tt.name, func(t *testing.T) {
			op := entity.TechCardOperation{OperationType: entity.OpTypeMachine, Zone: "outer"}
			tt.fill(&op)
			require.True(t, storedHasMachineFacts(&entity.TechCard{
				TechCardInsert: entity.TechCardInsert{Operations: []entity.TechCardOperation{op}},
			}), "%s must count as a machine fact", tt.name)
		})
	}
	t.Run("press_steam NULL is not a fact", func(t *testing.T) {
		require.False(t, storedHasMachineFacts(&entity.TechCard{
			TechCardInsert: entity.TechCardInsert{Operations: []entity.TechCardOperation{{
				OperationType: entity.OpTypeFusing, Zone: "outer", PressSteam: sql.NullBool{},
			}}},
		}), "«not stated» is an absence and must leave the old bundle alone")
	})
	t.Run("an empty park is not a fact", func(t *testing.T) {
		require.False(t, storedHasMachineFacts(&entity.TechCard{TechCardInsert: entity.TechCardInsert{
			Construction: &entity.TechCardConstruction{EquipmentDefaults: &entity.TechCardEquipmentDefaults{}},
		}}))
	})
}

// --- §9: the fusing sign gate ---------------------------------------------------------------------

func gateConstructionSignoff() []*pb_common.TechCardSignoff {
	return []*pb_common.TechCardSignoff{{
		Section: pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_CONSTRUCTION,
		State:   pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED,
	}}
}

func gateFusingStep(profileKey string) *pb_common.TechCardOperation {
	return &pb_common.TechCardOperation{
		OperationType:   pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING,
		Zone:            gateZone(),
		PressEquipment:  pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_FUSING_PRESS,
		PressProfileKey: profileKey,
	}
}

func gateFusingPressProfile(key string, temp, dwell int32, process pb_common.TechCardOperationType) *pb_common.TechCardPressProfile {
	return &pb_common.TechCardPressProfile{
		ProfileKey:        key,
		PressEquipment:    pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_FUSING_PRESS,
		OperationType:     process,
		PressTemperatureC: temp,
		PressDwellSec:     dwell,
	}
}

// gateFusingCard builds an aware payload approving CONSTRUCTION over the given park and steps.
func gateFusingCard(presses []*pb_common.TechCardPressProfile, ops ...*pb_common.TechCardOperation) *pb_common.TechCardInsert {
	tc := gateInsert(true)
	tc.Construction = &pb_common.TechCardConstruction{
		EquipmentDefaults: &pb_common.TechCardEquipmentDefaults{Presses: presses},
	}
	tc.Operations = ops
	tc.Signoffs = gateConstructionSignoff()
	return tc
}

func TestUpdateTechCardRefusesApprovingFusingWithoutTemperatureOrDwell(t *testing.T) {
	tests := []struct {
		name    string
		presses []*pb_common.TechCardPressProfile
		step    *pb_common.TechCardOperation
	}{
		{"no park at all", nil, gateFusingStep("")},
		{
			"a profile that states neither value",
			[]*pb_common.TechCardPressProfile{gateFusingPressProfile(gateFusingKeyA, 0, 0,
				pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING)},
			gateFusingStep(gateFusingKeyA),
		},
		{
			// A press profile is not a fusing recipe: same equipment, different process.
			"the only matching profile is declared for pressing",
			[]*pb_common.TechCardPressProfile{gateFusingPressProfile(gateFusingKeyA, 150, 12,
				pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS)},
			gateFusingStep(""),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := gateUpdate(t, &entity.TechCard{}, gateFusingCard(tt.presses, tt.step), gateStopsAtStored)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			msg := status.Convert(err).Message()
			require.Contains(t, msg, "signoffs[0]")
			require.Contains(t, msg, "10", "the refusal must name the step that is short")
			require.Contains(t, msg, "press_temperature_c")
		})
	}
}

// Half a recipe is not a recipe: a temperature with no dwell is refused exactly like neither.
func TestUpdateTechCardRefusesApprovingFusingWithOnlyHalfTheRecipe(t *testing.T) {
	step := gateFusingStep("")
	step.PressTemperatureC = 150
	err := gateUpdate(t, &entity.TechCard{}, gateFusingCard(nil, step), gateStopsAtStored)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "fusing step(s) 10")
}

func TestUpdateTechCardApprovesFusingResolvedThroughTheLadder(t *testing.T) {
	fusing := gateFusingPressProfile(gateFusingKeyA, 150, 12,
		pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING)
	universal := gateFusingPressProfile(gateFusingKeyB, 140, 10,
		pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_UNKNOWN)
	tests := []struct {
		name    string
		presses []*pb_common.TechCardPressProfile
		step    func() *pb_common.TechCardOperation
	}{
		{"the step states both values itself", nil, func() *pb_common.TechCardOperation {
			s := gateFusingStep("")
			s.PressTemperatureC, s.PressDwellSec = 150, 12
			return s
		}},
		{"the only fusing profile of that equipment", []*pb_common.TechCardPressProfile{fusing},
			func() *pb_common.TechCardOperation { return gateFusingStep("") }},
		{"a universal profile counts as fitting", []*pb_common.TechCardPressProfile{universal},
			func() *pb_common.TechCardOperation { return gateFusingStep("") }},
		{"two profiles, and the step names one by key", []*pb_common.TechCardPressProfile{fusing, universal},
			func() *pb_common.TechCardOperation { return gateFusingStep(gateFusingKeyB) }},
		{
			// Two profiles of the same equipment are not ambiguous when only one is for fusing.
			"a pressing profile does not compete for a fusing step",
			[]*pb_common.TechCardPressProfile{fusing, gateFusingPressProfile(gateFusingKeyB, 200, 5,
				pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS)},
			func() *pb_common.TechCardOperation { return gateFusingStep("") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := gateUpdate(t, &entity.TechCard{}, gateFusingCard(tt.presses, tt.step()), gateReachesStore)
			require.Equal(t, codes.Aborted, status.Code(err))
		})
	}
}

// TestUpdateTechCardRefusesApprovingFusingThroughAPressingProfileNamedByKey closes the hole step 2
// of the ladder used to be. Step 3 always refused a profile declared for pressing; step 2 took
// whatever the key pointed at, so the same park answered one question two ways and the softer answer
// was reachable by simply putting a key on the step — including BY THE SERVER, which is how it was
// actually reached: the AI mapper attached the card's only ironing profile of the дублирующий пресс
// to a drafted fusing step, and the gate then read that profile's temperature and dwell back out
// through the key and approved. Дублирование on an ironing program, signed.
func TestUpdateTechCardRefusesApprovingFusingThroughAPressingProfileNamedByKey(t *testing.T) {
	ironing := gateFusingPressProfile(gateFusingKeyA, 150, 3,
		pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS)
	fusing := gateFusingPressProfile(gateFusingKeyB, 145, 12,
		pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING)

	tests := []struct {
		name    string
		presses []*pb_common.TechCardPressProfile
	}{
		{"the card's only profile of that press is an ironing one", []*pb_common.TechCardPressProfile{ironing}},
		{
			// And the refusal is not «try the next one»: the step NAMES this profile, and signing it
			// off against a different profile of the same press would be a second wrong answer — the
			// sheet would print the values of the row nobody pointed at.
			"a mismatched key is not quietly swapped for the profile that would have fitted",
			[]*pb_common.TechCardPressProfile{ironing, fusing},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := gateUpdate(t, &entity.TechCard{},
				gateFusingCard(tt.presses, gateFusingStep(gateFusingKeyA)), gateStopsAtStored)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			msg := status.Convert(err).Message()
			require.Contains(t, msg, "signoffs[0]")
			require.Contains(t, msg, "fusing step(s) 10")
			require.Contains(t, msg, "press_temperature_c")
		})
	}

	// The other direction, so the narrowing cannot be «refuse everything named by key»: a profile
	// that declares no process is universal and a step naming it by key still resolves through it.
	t.Run("a universal profile named by key still signs the step off", func(t *testing.T) {
		universal := gateFusingPressProfile(gateFusingKeyB, 140, 10,
			pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_UNKNOWN)
		err := gateUpdate(t, &entity.TechCard{},
			gateFusingCard([]*pb_common.TechCardPressProfile{ironing, universal}, gateFusingStep(gateFusingKeyB)),
			gateReachesStore)
		require.Equal(t, codes.Aborted, status.Code(err))
	})

	// …and a profile declared FOR fusing, named by key, is exactly what step 2 is for.
	t.Run("a fusing profile named by key signs the step off", func(t *testing.T) {
		err := gateUpdate(t, &entity.TechCard{},
			gateFusingCard([]*pb_common.TechCardPressProfile{ironing, fusing}, gateFusingStep(gateFusingKeyB)),
			gateReachesStore)
		require.Equal(t, codes.Aborted, status.Code(err))
	})
}

func TestUpdateTechCardRefusesApprovingFusingWithTwoFittingProfiles(t *testing.T) {
	presses := []*pb_common.TechCardPressProfile{
		gateFusingPressProfile(gateFusingKeyA, 150, 12,
			pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING),
		gateFusingPressProfile(gateFusingKeyB, 190, 20,
			pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_UNKNOWN),
	}
	err := gateUpdate(t, &entity.TechCard{}, gateFusingCard(presses, gateFusingStep("")), gateStopsAtStored)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	msg := status.Convert(err).Message()
	require.Contains(t, msg, "signoffs[0]")
	require.Contains(t, msg, "more than one")
	require.Contains(t, msg, "press_profile_key")
}

// A draft is never blocked — the gate is on the SIGNATURE and nowhere else.
func TestUpdateTechCardSavesAnUnderspecifiedFusingStepWithoutApproval(t *testing.T) {
	tc := gateFusingCard(nil, gateFusingStep(""))
	tc.Signoffs = nil
	err := gateUpdate(t, &entity.TechCard{}, tc, gateReachesStore)
	require.Equal(t, codes.Aborted, status.Code(err))
}

// A CARRIED approval is storage's, not this request's: re-saving a card whose CONSTRUCTION was
// signed long ago must not be re-judged, or every save of an old card would need the new recipe.
func TestUpdateTechCardDoesNotReJudgeACarriedConstructionApproval(t *testing.T) {
	tc := gateFusingCard(nil, gateFusingStep(""))
	tc.Signoffs[0].SignedDigest = "carry-claim"
	stored := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		Signoffs: []entity.TechCardSignoff{{
			Section:      entity.SignoffConstruction,
			State:        entity.SignoffStateApproved,
			SignedBy:     sql.NullString{String: "original-approver", Valid: true},
			SignedDigest: sql.NullString{String: "stored-digest", Valid: true},
		}},
	}}
	err := gateUpdate(t, stored, tc, gateReachesStore)
	require.Equal(t, codes.Aborted, status.Code(err))
}

func TestCreateTechCardRefusesApprovingFusingWithoutTemperatureOrDwell(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	_, err := (&Server{repo: repo}).CreateTechCard(fullAccessCtx(), &pb_admin.CreateTechCardRequest{
		TechCard: gateFusingCard(nil, gateFusingStep("")),
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "fusing step(s) 10")
}

func TestCreateTechCardApprovesFusingResolvedThroughAProfile(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	// A field violation from the store is the cheapest recognisable «you got here»: it short-circuits
	// the error mapping above the IsErr* predicates, which this mock does not stub.
	techCards.EXPECT().AddTechCard(mock.Anything, mock.AnythingOfType("*entity.TechCardInsert")).
		Return(0, entity.NewFieldViolation("style_number", "reached the store", "", ""))

	presses := []*pb_common.TechCardPressProfile{gateFusingPressProfile(gateFusingKeyA, 150, 12,
		pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING)}
	_, err := (&Server{repo: repo}).CreateTechCard(fullAccessCtx(), &pb_admin.CreateTechCardRequest{
		TechCard: gateFusingCard(presses, gateFusingStep("")),
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "reached the store")
}
