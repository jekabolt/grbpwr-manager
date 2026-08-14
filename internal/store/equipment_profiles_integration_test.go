package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestTechCardEquipmentProfiles is the acceptance suite for the card's equipment park (0306): the
// machines a style is sewn on and the ВТО modes it is pressed in, plus the fifteen «на чём» columns
// a step overrides them with.
//
// The probe that decides whether the design is right is the presence one. The park is full-replaced
// like every other list, but its presence signal sits one level deeper than a section: an older
// bundle that knows nothing about profiles still sends a CONSTRUCTION, and if «no wrapper» were read
// as «no profiles» that bundle would silently wipe the park on every save it makes. So three
// distinct payloads have three distinct meanings and each is proved here — wrapper with content
// (replace), wrapper absent (preserve), wrapper present and empty (delete them all).
//
// SAFE ONLY against a local container DSN — see the guard and mysql_test.go / project memory
// (store-tests-drop-prod-db: the non-CI TestMain talks to the configured prod DB and DROPs tables).
func TestTechCardEquipmentProfiles(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
	T := s.TechCards()

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	ni := func(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }
	nb := func(v bool) sql.NullBool { return sql.NullBool{Bool: v, Valid: true} }
	nd := func(v string) decimal.NullDecimal {
		return decimal.NewNullDecimal(decimal.RequireFromString(v))
	}
	eqDec := func(t *testing.T, want string, got decimal.NullDecimal, what string) {
		t.Helper()
		require.True(t, got.Valid, "%s must round-trip as set", what)
		require.True(t, got.Decimal.Equal(decimal.RequireFromString(want)),
			"%s: want %s, got %s", what, want, got.Decimal.String())
	}
	// profile_key is CHAR(26) and carries the same durable-key contract as bom_line_key: minted by
	// the client, round-tripped verbatim, never re-derived. Padded here instead of hand-counted so a
	// typo cannot quietly produce a 25-character key that MySQL then pads for us.
	key := func(name string) string {
		require.LessOrEqual(t, len(name), 26)
		return name + strings.Repeat("0", 26-len(name))
	}
	keyWindow := key("OVERLOCKWINDOW")
	keyDoor := key("OVERLOCKDOOR")
	keyFusing := key("FUSINGPRESS")

	countProfiles := func(cardID int) int {
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM tech_card_equipment_profile WHERE tech_card_id = ?`, cardID).Scan(&n))
		return n
	}

	// Two overlocks on one card is the ordinary case, not a duplicate to collapse — «два одинаковых
	// станка» is what makes the machine TYPE unusable as an identity and the key load-bearing.
	machineWindow := entity.TechCardMachineProfile{
		ProfileKey: keyWindow, Label: ns("оверлок у окна"), MachineType: "overlock",
		ThreadCount: ni(4), NeedleType: ns("ballpoint"), NeedleSizeNm: ni(90),
		BedType: ns("flatbed"), Automation: ns("semi_auto"),
		ThreadTension: ns("looser"), ThreadTensionNote: ns("на 0.5 слабее"),
		AttachmentKind: ns("none"), StitchesPerCm: nd("4.00"), StitchWidthMm: nd("5.0"),
		Note: ns("Juki MO-6716"),
	}
	machineDoor := entity.TechCardMachineProfile{
		ProfileKey: keyDoor, Label: ns("оверлок у двери"), MachineType: "overlock",
		ThreadCount: ni(3),
	}
	pressFusing := entity.TechCardPressProfile{
		ProfileKey: keyFusing, Label: ns("дублирующий пресс"), PressEquipment: "fusing_press",
		PressOperationType: ns("fusing"), PressTemperatureC: ni(140), PressDwellSec: ni(12),
		PressPressureNCm2: nd("3.5"),
		// FALSE, not unset: «без пара» is an instruction, and the column is three-valued precisely so
		// the read below can tell it from «not stated».
		PressSteam: nb(false), PressCloth: ns("none"), Note: ns("Veit 1200"),
	}

	construction := func(hem string, d *entity.TechCardEquipmentDefaults) *entity.TechCardConstruction {
		return &entity.TechCardConstruction{
			HemFinish: ns(hem), Notes: ns("общие заметки"),
			DefaultSeamClass: ns("ss_plain"), DefaultStitchesPerCm: nd("4.00"),
			EquipmentDefaults: d,
		}
	}

	// A machine step and a ВТО step, each carrying its own block of overrides AND a soft reference to
	// a profile by key.
	machineStep := func(profileKey string) entity.TechCardOperation {
		return entity.TechCardOperation{
			OperationNumber: ni(10), OperationType: entity.OpTypeMachine, Zone: entity.ZoneOuter,
			SMV: nd("0.8"), Note: ns("обметать боковой шов"),
			MachineType: ns("overlock"), MachineProfileKey: ns(profileKey),
			ThreadCount: ni(3), NeedleType: ns("stretch"), NeedleSizeNm: ni(75),
			ThreadTension: ns("tighter"), ThreadTensionNote: ns("на 0.5 туже"),
			StitchWidthMm: nd("4.5"),
		}
	}
	pressStep := entity.TechCardOperation{
		OperationNumber: ni(20), OperationType: entity.OpTypeFusing, Zone: entity.ZoneInterlining,
		PressEquipment: ns("fusing_press"), PressProfileKey: ns(keyFusing),
		PressTemperatureC: ni(150), PressDwellSec: ni(15), PressPressureNCm2: nd("4.0"),
		PressSteam: nb(false), PressCloth: ns("teflon_sheet"),
	}

	card := func(styleNumber string, c *entity.TechCardConstruction, ops []entity.TechCardOperation) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "Equipment Park Style", Stage: entity.TechCardStageProto,
			StyleNumber: ns(styleNumber), Purpose: entity.TechCardPurposeSellable,
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			SeasonCode: ns("SS"), SeasonYear: ni(2026),
			Construction: c, Operations: ops,
		}
	}

	// --- A. create: the park and the two steps land -------------------------------------------------
	tcID, err := T.AddTechCard(ctx, card("EQP-T4-1",
		construction("подгибка 2 см", &entity.TechCardEquipmentDefaults{
			Machines: []entity.TechCardMachineProfile{machineWindow, machineDoor},
			Presses:  []entity.TechCardPressProfile{pressFusing},
		}),
		[]entity.TechCardOperation{machineStep(keyDoor), pressStep}))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	// A neighbouring card with its own park: every DELETE and every read below is scoped by
	// tech_card_id, and this is what proves it rather than asserting it.
	otherID, err := T.AddTechCard(ctx, card("EQP-T4-2",
		construction("другой низ", &entity.TechCardEquipmentDefaults{
			Machines: []entity.TechCardMachineProfile{machineWindow},
		}), nil))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", otherID) })

	c1, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.NotNil(t, c1.Construction)
	require.Equal(t, "подгибка 2 см", c1.Construction.HemFinish.String)
	require.Equal(t, "общие заметки", c1.Construction.Notes.String, "construction still saves after 0306 dropped pressing/overlock_thread_count")
	d1 := c1.Construction.EquipmentDefaults
	require.NotNil(t, d1, "a card with profiles must read back with a park")
	require.Len(t, d1.Machines, 2)
	require.Len(t, d1.Presses, 1)
	require.Equal(t, []string{keyDoor, keyWindow},
		[]string{d1.Machines[0].ProfileKey, d1.Machines[1].ProfileKey},
		"machines read back ordered by profile_key")

	window := d1.Machines[1]
	require.Equal(t, "оверлок у окна", window.Label.String)
	require.Equal(t, "overlock", window.MachineType)
	require.Equal(t, int32(4), window.ThreadCount.Int32)
	require.Equal(t, "ballpoint", window.NeedleType.String)
	require.Equal(t, int32(90), window.NeedleSizeNm.Int32)
	require.Equal(t, "flatbed", window.BedType.String)
	require.Equal(t, "semi_auto", window.Automation.String)
	require.Equal(t, "looser", window.ThreadTension.String)
	require.Equal(t, "на 0.5 слабее", window.ThreadTensionNote.String)
	require.Equal(t, "none", window.AttachmentKind.String, "'none' is a stored token, not a NULL")
	eqDec(t, "4.00", window.StitchesPerCm, "profile stitches_per_cm")
	eqDec(t, "5.0", window.StitchWidthMm, "profile stitch_width_mm")
	require.Equal(t, "Juki MO-6716", window.Note.String)
	require.NotZero(t, window.Id, "the row id is read for callers that need it")
	require.Equal(t, tcID, window.TechCardId)
	// The press half of a machine row is NULL by construction — the entity has nowhere to put it.
	require.Equal(t, "overlock", d1.Machines[0].MachineType, "the second overlock is a profile in its own right")
	require.Equal(t, int32(3), d1.Machines[0].ThreadCount.Int32)

	press := d1.Presses[0]
	require.Equal(t, keyFusing, press.ProfileKey)
	require.Equal(t, "дублирующий пресс", press.Label.String)
	require.Equal(t, "fusing_press", press.PressEquipment)
	require.Equal(t, "fusing", press.PressOperationType.String)
	require.Equal(t, int32(140), press.PressTemperatureC.Int32)
	require.Equal(t, int32(12), press.PressDwellSec.Int32)
	eqDec(t, "3.5", press.PressPressureNCm2, "profile press_pressure_n_cm2")
	require.True(t, press.PressSteam.Valid, "«без пара» is stated, not absent")
	require.False(t, press.PressSteam.Bool)
	require.Equal(t, "none", press.PressCloth.String)
	require.Equal(t, "Veit 1200", press.Note.String)

	opByNumber := func(c *entity.TechCard) map[int32]entity.TechCardOperation {
		out := make(map[int32]entity.TechCardOperation, len(c.Operations))
		for _, o := range c.Operations {
			out[o.OperationNumber.Int32] = o
		}
		return out
	}
	ops1 := opByNumber(c1)
	require.Len(t, ops1, 2)
	m := ops1[10]
	require.Equal(t, entity.OpTypeMachine, m.OperationType)
	require.Equal(t, "overlock", m.MachineType.String)
	require.Equal(t, keyDoor, m.MachineProfileKey.String)
	require.Equal(t, int32(3), m.ThreadCount.Int32)
	require.Equal(t, "stretch", m.NeedleType.String)
	require.Equal(t, int32(75), m.NeedleSizeNm.Int32)
	require.Equal(t, "tighter", m.ThreadTension.String)
	require.Equal(t, "на 0.5 туже", m.ThreadTensionNote.String)
	eqDec(t, "4.5", m.StitchWidthMm, "step stitch_width_mm")
	require.False(t, m.PressEquipment.Valid, "a machine step carries no ВТО block")
	require.False(t, m.PressSteam.Valid)

	p := ops1[20]
	require.Equal(t, entity.OpTypeFusing, p.OperationType)
	require.Equal(t, "fusing_press", p.PressEquipment.String)
	require.Equal(t, keyFusing, p.PressProfileKey.String)
	require.Equal(t, int32(150), p.PressTemperatureC.Int32)
	require.Equal(t, int32(15), p.PressDwellSec.Int32)
	eqDec(t, "4.0", p.PressPressureNCm2, "step press_pressure_n_cm2")
	require.True(t, p.PressSteam.Valid, "step «без пара» survives as an explicit false")
	require.False(t, p.PressSteam.Bool)
	require.Equal(t, "teflon_sheet", p.PressCloth.String)
	require.False(t, p.MachineType.Valid, "a ВТО step carries no machine block")

	// --- B. full replace: keys are stable, a dropped profile is gone --------------------------------
	renamedWindow := machineWindow
	renamedWindow.Label = ns("оверлок у окна (новый)")
	require.NoError(t, T.UpdateTechCard(ctx, tcID, card("EQP-T4-1",
		construction("подгибка 2 см", &entity.TechCardEquipmentDefaults{
			Machines: []entity.TechCardMachineProfile{renamedWindow},
			Presses:  []entity.TechCardPressProfile{pressFusing},
		}),
		[]entity.TechCardOperation{machineStep(keyDoor), pressStep}), c1.LockVersion))

	c2, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	d2 := c2.Construction.EquipmentDefaults
	require.NotNil(t, d2)
	require.Len(t, d2.Machines, 1, "the dropped overlock is gone — the list is a full replace")
	require.Equal(t, keyWindow, d2.Machines[0].ProfileKey, "the durable key survives the replace verbatim")
	require.Equal(t, "оверлок у окна (новый)", d2.Machines[0].Label.String)
	require.Len(t, d2.Presses, 1)
	require.Equal(t, keyFusing, d2.Presses[0].ProfileKey)
	require.Equal(t, 2, countProfiles(tcID))
	require.Equal(t, 1, countProfiles(otherID), "the neighbouring card's park is untouched")

	ops2 := opByNumber(c2)
	require.Equal(t, int32(3), ops2[10].ThreadCount.Int32, "step overrides survive the full replace")
	require.Equal(t, "на 0.5 туже", ops2[10].ThreadTensionNote.String)
	// The step still points at the profile that was just deleted, and the store leaves it exactly as
	// it was: the reference is SOFT by design (no FK, no cascade, no tidying here). Detaching a key
	// that names nothing is the write converter's job, where the payload's own park is in hand.
	require.Equal(t, keyDoor, ops2[10].MachineProfileKey.String,
		"a key naming no profile is kept verbatim — the store resolves nothing")
	eqDec(t, "4.0", ops2[20].PressPressureNCm2, "step press pressure survives the full replace")

	// --- C. wrapper ABSENT: the section is replaced, the park is preserved ---------------------------
	require.NoError(t, T.UpdateTechCard(ctx, tcID, card("EQP-T4-1",
		construction("подгибка 4 см", nil),
		[]entity.TechCardOperation{machineStep(keyWindow), pressStep}), c2.LockVersion))

	c3, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Equal(t, "подгибка 4 см", c3.Construction.HemFinish.String,
		"the construction row itself WAS replaced — only the park was spared")
	require.NotNil(t, c3.Construction.EquipmentDefaults, "a payload that does not speak about equipment must not erase it")
	require.Len(t, c3.Construction.EquipmentDefaults.Machines, 1)
	require.Equal(t, keyWindow, c3.Construction.EquipmentDefaults.Machines[0].ProfileKey)
	require.Len(t, c3.Construction.EquipmentDefaults.Presses, 1)
	require.Equal(t, 2, countProfiles(tcID))

	// --- D. no construction at all: both the row and the park stand ---------------------------------
	require.NoError(t, T.UpdateTechCard(ctx, tcID, card("EQP-T4-1", nil,
		[]entity.TechCardOperation{machineStep(keyWindow), pressStep}), c3.LockVersion))

	c4, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.NotNil(t, c4.Construction)
	require.Equal(t, "подгибка 4 см", c4.Construction.HemFinish.String, "the stored section survived an absent one")
	require.NotNil(t, c4.Construction.EquipmentDefaults)
	require.Equal(t, 2, countProfiles(tcID))

	// --- E. EMPTY wrapper: delete them all ----------------------------------------------------------
	require.NoError(t, T.UpdateTechCard(ctx, tcID, card("EQP-T4-1",
		construction("подгибка 4 см", &entity.TechCardEquipmentDefaults{}),
		[]entity.TechCardOperation{machineStep(keyWindow), pressStep}), c4.LockVersion))

	c5, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.NotNil(t, c5.Construction)
	require.Nil(t, c5.Construction.EquipmentDefaults, "no profiles reads back as no park")
	require.Equal(t, 0, countProfiles(tcID), "an empty wrapper is a deliberate delete, not a no-op")
	require.Equal(t, 1, countProfiles(otherID), "and it is scoped to this card")
	require.Len(t, c5.Operations, 2, "the steps and their overrides are untouched by the park's deletion")
	require.Equal(t, keyWindow, opByNumber(c5)[10].MachineProfileKey.String)

	// --- F. profiles with no construction row (the shape 0306 leaves behind) -------------------------
	// The migration's 8a mints an overlock profile for every card that had an overlock_thread_count,
	// and nothing guarantees such a card also has a construction row on the read path — a later save
	// could clear it. The park must not vanish with it, so the reader synthesises an empty section.
	_, err = testDB.ExecContext(ctx, `DELETE FROM tech_card_construction WHERE tech_card_id = ?`, tcID)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `
		INSERT INTO tech_card_equipment_profile (tech_card_id, profile_key, kind, equipment, thread_count)
		VALUES (?, CONCAT('LEGACYOVERLOCK', LPAD(?, 12, '0')), 'machine', 'overlock', 4)`, tcID, tcID)
	require.NoError(t, err)

	c6, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.NotNil(t, c6.Construction, "profiles with no construction row still need somewhere to live")
	require.False(t, c6.Construction.HemFinish.Valid, "the synthesised section is empty, not invented")
	require.NotNil(t, c6.Construction.EquipmentDefaults)
	require.Len(t, c6.Construction.EquipmentDefaults.Machines, 1)
	require.Equal(t, int32(4), c6.Construction.EquipmentDefaults.Machines[0].ThreadCount.Int32)

	// --- G. the pair (kind, equipment) is refused in Go, in a sentence ------------------------------
	// 0306 checks the two vocabularies as a UNION in one shared column, so the database accepts
	// (kind='machine', equipment='iron') without a word. Nothing downstream could recover: the read
	// path would hand a press back as a machine type and the mapper would drop it to UNKNOWN.
	var ve *entity.ValidationError

	ironAsMachine := machineWindow
	ironAsMachine.MachineType = "iron"
	err = T.UpdateTechCard(ctx, tcID, card("EQP-T4-1",
		construction("подгибка 4 см", &entity.TechCardEquipmentDefaults{
			Machines: []entity.TechCardMachineProfile{ironAsMachine},
		}), nil), c6.LockVersion)
	require.Error(t, err, "an iron is not a sewing machine")
	require.True(t, errors.As(err, &ve), "the refusal must be field-tagged, got %v", err)

	c7, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Len(t, c7.Construction.EquipmentDefaults.Machines, 1,
		"the refused save rolled back — the legacy profile is still there")

	overlockAsPress := pressFusing
	overlockAsPress.PressEquipment = "overlock"
	err = T.UpdateTechCard(ctx, tcID, card("EQP-T4-1",
		construction("подгибка 4 см", &entity.TechCardEquipmentDefaults{
			Presses: []entity.TechCardPressProfile{overlockAsPress},
		}), nil), c7.LockVersion)
	require.Error(t, err, "an overlock is not pressing equipment")
	require.True(t, errors.As(err, &ve), "the refusal must be field-tagged, got %v", err)

	c8, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	keyless := machineWindow
	keyless.ProfileKey = "   "
	err = T.UpdateTechCard(ctx, tcID, card("EQP-T4-1",
		construction("подгибка 4 см", &entity.TechCardEquipmentDefaults{
			Machines: []entity.TechCardMachineProfile{keyless},
		}), nil), c8.LockVersion)
	require.Error(t, err, "a profile no step can reference is not a profile")
	require.True(t, errors.As(err, &ve), "the refusal must be field-tagged, got %v", err)
}
