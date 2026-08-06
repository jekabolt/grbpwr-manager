package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestBomLinePurpose covers 0265: НАЗНАЧЕНИЕ, its OTHER note and the семпловая flag round-trip on a
// BOM line; purpose is optional; and a line that never carried one keeps NULL rather than acquiring
// a guessed value.
//
// The last of those is the point of the whole design and the easiest thing to regress. section
// ='fabric' is exactly where a pocket-bag, a contrast and a mesh second layer hide, so any code that
// helpfully defaults an unset purpose to "основной материал" would label all three confidently and
// wrongly, and the operator would have no way to tell a sorted card from an unsorted one. NULL has
// to survive a full write→read→write cycle untouched, including the cycle where a NEIGHBOURING line
// on the same card is edited — that is the save shape the admin actually produces.
func TestBomLinePurpose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()

	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&sizeID))

	const (
		slotMain     = "01BPMAIN0000000000000000P1"
		slotPocket   = "01BPPOCKETING00000000000P2"
		slotOther    = "01BPOTHER000000000000000P3"
		slotLegacy   = "01BPLEGACY0000000000000P4"
		slotSampleLn = "01BPSAMPLELINING0000000P5"
		slotThread   = "01BPTHREAD000000000000P6"
	)
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	purpose := func(p entity.TechCardBomPurpose) sql.NullString { return ns(string(p)) }

	bom := []entity.TechCardBomItem{
		{LineKey: slotMain, Section: entity.BomSectionFabric, Name: "основная",
			Purpose: purpose(entity.BomPurposeMain)},
		// Same section, different role — the distinction the field exists for.
		{LineKey: slotPocket, Section: entity.BomSectionFabric, Name: "карманка",
			Purpose: purpose(entity.BomPurposePocketing)},
		// OTHER carries its meaning in the separate note column.
		{LineKey: slotOther, Section: entity.BomSectionFabric, Name: "лента-корсаж",
			Purpose: purpose(entity.BomPurposeOther), PurposeNote: ns("проклад под пояс")},
		// Purpose is optional: a line may be saved unsorted, and stays unsorted.
		{LineKey: slotLegacy, Section: entity.BomSectionFabric, Name: "не разобрана"},
		// Семпловая is a flag beside a real purpose, not a purpose of its own — this is a sample
		// LINING and must still read as lining.
		{LineKey: slotSampleLn, Section: entity.BomSectionLining, Name: "подкладка на семпл",
			Purpose: purpose(entity.BomPurposeLining), IsSample: true},
		// Not roll goods, so no purpose — but the sample flag is a property of the yardage, not of
		// the grouping, and is accepted anywhere.
		{LineKey: slotThread, Section: entity.BomSectionThread, Name: "нитки"},
	}

	mk := func(items []entity.TechCardBomItem) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "BOM PURPOSE", StyleNumber: ns("BP-1"),
			Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			SizeIds:         []int{sizeID},
			BomItems:        items,
		}
	}

	tcID, err := T.AddTechCard(ctx, mk(bom))
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), tcID) })

	byKey := func() map[string]entity.TechCardBomItem {
		t.Helper()
		tc, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		m := make(map[string]entity.TechCardBomItem, len(tc.BomItems))
		for _, b := range tc.BomItems {
			m[b.LineKey] = b
		}
		require.Len(t, m, len(bom))
		return m
	}

	got := byKey()

	require.Equal(t, string(entity.BomPurposeMain), got[slotMain].Purpose.String)
	require.False(t, got[slotMain].PurposeNote.Valid)
	require.False(t, got[slotMain].IsSample)

	require.Equal(t, string(entity.BomPurposePocketing), got[slotPocket].Purpose.String)

	require.Equal(t, string(entity.BomPurposeOther), got[slotOther].Purpose.String)
	require.Equal(t, "проклад под пояс", got[slotOther].PurposeNote.String)

	// Optional: an unsorted line reads back honestly unset, not defaulted.
	require.False(t, got[slotLegacy].Purpose.Valid, "an unsorted line must stay unsorted")
	require.False(t, got[slotLegacy].PurposeNote.Valid)
	require.False(t, got[slotLegacy].IsSample)

	// A sample line keeps its real purpose; the flag is the only thing that says "sample".
	require.Equal(t, string(entity.BomPurposeLining), got[slotSampleLn].Purpose.String)
	require.True(t, got[slotSampleLn].IsSample)

	require.False(t, got[slotThread].Purpose.Valid)

	// A save that edits ONE line must not disturb the unsorted neighbour. This is the shape the admin
	// produces on every edit (the whole BOM is resent), so a defaulting bug would surface here.
	lockVersion := func() int {
		tc, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		return tc.LockVersion
	}
	edited := make([]entity.TechCardBomItem, len(bom))
	copy(edited, bom)
	edited[0].Purpose = purpose(entity.BomPurposeContrast)
	edited[0].IsSample = true
	// Clearing an OTHER line's purpose must take its note with it — the note is meaningless alone and
	// chk_bom_item_purpose_note would refuse to store it.
	edited[2].Purpose = sql.NullString{}
	edited[2].PurposeNote = sql.NullString{}
	require.NoError(t, T.UpdateTechCard(ctx, tcID, mk(edited), lockVersion()))

	// А ТЕПЕРЬ САМА ГАРАНТИЯ, мимо приложения. Очевидная запись CHECK (`purpose = 'other'`) при
	// purpose IS NULL даёт NULL, а NULL MySQL считает выполненным условием — то есть ловит дырку
	// purpose='main' и пропускает дырку purpose IS NULL, которая и есть состояние каждой
	// неразложенной строки. Проверяем ОБА плеча прямым SQL: через dto такая запись не пройдёт, но
	// констрейнт заводился как последний рубеж, а не как дубль валидации.
	_, err = testDB.ExecContext(ctx,
		`UPDATE tech_card_bom_item SET purpose = NULL, purpose_note = 'теневое назначение'
		 WHERE tech_card_id = ? AND line_key = ?`, tcID, slotLegacy)
	require.Error(t, err, "примечание без назначения (purpose NULL) должно отвергаться")
	_, err = testDB.ExecContext(ctx,
		`UPDATE tech_card_bom_item SET purpose = 'main', purpose_note = 'теневое назначение'
		 WHERE tech_card_id = ? AND line_key = ?`, tcID, slotLegacy)
	require.Error(t, err, "примечание при назначении не-other должно отвергаться")
	// И регистр: REGEXP под utf8mb3_general_ci регистронезависим, так что 'MAIN' прошёл бы и не
	// попал бы потом ни в одну группу интерфейса.
	_, err = testDB.ExecContext(ctx,
		`UPDATE tech_card_bom_item SET purpose = 'MAIN' WHERE tech_card_id = ? AND line_key = ?`,
		tcID, slotLegacy)
	require.Error(t, err, "назначение в другом регистре должно отвергаться")

	// СТАРЫЙ КЛИЕНТ НЕ ДОЛЖЕН СТИРАТЬ. Вкладка с бандлом до 0265 этих полей не шлёт вовсе; на
	// проводе это ОТСУТСТВИЕ, а не UNSET, и стор обязан оставить хранимое. Без различения один сейв
	// из открытой вчера вкладки обнулял бы назначение у ВСЕХ строк карточки — бесследно, потому что
	// полей нет в дайджесте подписи, а NULL неотличим от «ещё не разложили».
	stale := make([]entity.TechCardBomItem, len(edited))
	copy(stale, edited)
	for i := range stale {
		stale[i].Purpose = sql.NullString{}
		stale[i].PurposeNote = sql.NullString{}
		stale[i].IsSample = false
		stale[i].PurposeOmitted = true
		stale[i].IsSampleOmitted = true
	}
	require.NoError(t, T.UpdateTechCard(ctx, tcID, mk(stale), lockVersion()))
	afterStale := byKey()
	require.Equal(t, string(entity.BomPurposeContrast), afterStale[slotMain].Purpose.String,
		"сейв без поля обязан сохранить назначение, а не обнулить его")
	require.True(t, afterStale[slotMain].IsSample, "и признак «семпловая» тоже")
	require.Equal(t, string(entity.BomPurposeLining), afterStale[slotSampleLn].Purpose.String)

	got = byKey()
	require.Equal(t, string(entity.BomPurposeContrast), got[slotMain].Purpose.String)
	require.True(t, got[slotMain].IsSample)
	require.False(t, got[slotOther].Purpose.Valid)
	require.False(t, got[slotOther].PurposeNote.Valid)
	require.False(t, got[slotLegacy].Purpose.Valid, "editing a neighbour must not sort an unsorted line")
	require.Equal(t, string(entity.BomPurposeLining), got[slotSampleLn].Purpose.String)

	// A purpose on a line that is not roll goods is refused with a field-tagged error rather than
	// stored as data no screen will ever show.
	bad := make([]entity.TechCardBomItem, len(bom))
	copy(bad, bom)
	bad[5].Purpose = purpose(entity.BomPurposeMain)
	err = T.UpdateTechCard(ctx, tcID, mk(bad), lockVersion())
	require.Error(t, err)
	require.Contains(t, err.Error(), "roll goods")

	// The rejected save must not have half-applied: the card is exactly as the last good save left it.
	got = byKey()
	require.Equal(t, string(entity.BomPurposeContrast), got[slotMain].Purpose.String)
	require.False(t, got[slotThread].Purpose.Valid)
}
