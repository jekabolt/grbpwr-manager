package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestBomLineKind covers 0276: ЧТО ЭТО ЗА ПОЗИЦИЯ round-trips on a BOM line, the kind↔section
// pairing is refused in the store, the vocabulary and the note rule are refused by MySQL itself, and
// a line nobody has classified keeps NULL through a full write→read→write cycle.
//
// The mirror of TestBomLinePurpose, and it exists for the mirror reason. `kind` groups lines that
// section alone cannot tell apart — a zipper, a snap and a rivet are all section='hardware' — so any
// code that helpfully defaults an unclassified line would put every one of them in one confident,
// wrong bucket, and the operator would have no way to tell a classified card from an untouched one.
func TestBomLineKind(t *testing.T) {
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
		slotZip      = "01BKZIPPER0000000000000K1"
		slotVelcro   = "01BKHOOKLOOP0000000000K2"
		slotStud     = "01BKSTUD000000000000000K3"
		slotThread   = "01BKTHREAD000000000000K4"
		slotPolybag  = "01BKPOLYBAG00000000000K5"
		slotOther    = "01BKOTHER00000000000000K6"
		slotUnsorted = "01BKUNSORTED0000000000K7"
		slotFabric   = "01BKFABRIC000000000000K8"
		slotLabel    = "01BKLABEL0000000000000K9"
	)
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	kind := func(k entity.TechCardBomKind) sql.NullString { return ns(string(k)) }

	bom := []entity.TechCardBomItem{
		{LineKey: slotZip, Section: entity.BomSectionHardware, Name: "молния", Kind: kind(entity.BomKindZipper)},
		// Велкро — это TRIM, а не фурнитура: метражная лента. Ровно тот спорный случай, который
		// решён в словаре, а не в голове у оператора.
		{LineKey: slotVelcro, Section: entity.BomSectionTrim, Name: "велкро", Kind: kind(entity.BomKindHookLoop)},
		// Декоративная клёпка — decoration; несущая заклёпка того же вида — hardware/rivet.
		{LineKey: slotStud, Section: entity.BomSectionDecoration, Name: "клёпка", Kind: kind(entity.BomKindStud)},
		{LineKey: slotThread, Section: entity.BomSectionThread, Name: "нитки", Kind: kind(entity.BomKindSewingThread)},
		{LineKey: slotPolybag, Section: entity.BomSectionPackaging, Name: "полибэг", Kind: kind(entity.BomKindPolybag)},
		// `other` is legal in EVERY eligible section, including section='other', which has no kinds
		// of its own; its meaning lives in the separate note.
		{LineKey: slotOther, Section: entity.BomSectionOther, Name: "непонятное",
			Kind: kind(entity.BomKindOther), KindNote: ns("силиконовая вставка от поставщика")},
		// Kind is optional: a line may be saved unclassified, and stays unclassified.
		{LineKey: slotUnsorted, Section: entity.BomSectionHardware, Name: "не разобрана"},
		// Roll goods answer назначение instead, labels answer label_type — neither carries a kind.
		{LineKey: slotFabric, Section: entity.BomSectionFabric, Name: "ткань"},
		{LineKey: slotLabel, Section: entity.BomSectionLabel, Name: "этикетка"},
	}

	mk := func(items []entity.TechCardBomItem) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "BOM KIND", StyleNumber: ns("BK-1"),
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
	lockVersion := func() int {
		tc, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		return tc.LockVersion
	}

	got := byKey()
	require.Equal(t, string(entity.BomKindZipper), got[slotZip].Kind.String)
	require.False(t, got[slotZip].KindNote.Valid)
	require.Equal(t, string(entity.BomKindHookLoop), got[slotVelcro].Kind.String)
	require.Equal(t, string(entity.BomKindStud), got[slotStud].Kind.String)
	require.Equal(t, string(entity.BomKindSewingThread), got[slotThread].Kind.String)
	require.Equal(t, string(entity.BomKindPolybag), got[slotPolybag].Kind.String)
	require.Equal(t, string(entity.BomKindOther), got[slotOther].Kind.String)
	require.Equal(t, "силиконовая вставка от поставщика", got[slotOther].KindNote.String)

	// Unclassified reads back honestly unset, not defaulted — the point of the whole design.
	require.False(t, got[slotUnsorted].Kind.Valid, "an unclassified line must stay unclassified")
	require.False(t, got[slotFabric].Kind.Valid)
	require.False(t, got[slotLabel].Kind.Valid)

	// A save that edits ONE line must not disturb the unclassified neighbour. This is the shape the
	// admin produces on every edit (the whole BOM is resent), so a defaulting bug surfaces here.
	edited := make([]entity.TechCardBomItem, len(bom))
	copy(edited, bom)
	edited[0].Kind = kind(entity.BomKindButton)
	// Clearing an OTHER line's kind must take its note with it — the note is meaningless alone and
	// chk_bom_item_kind_note would refuse to store it.
	edited[5].Kind = sql.NullString{}
	edited[5].KindNote = sql.NullString{}
	require.NoError(t, T.UpdateTechCard(ctx, tcID, mk(edited), lockVersion()))

	got = byKey()
	require.Equal(t, string(entity.BomKindButton), got[slotZip].Kind.String)
	require.False(t, got[slotOther].Kind.Valid)
	require.False(t, got[slotOther].KindNote.Valid)
	require.False(t, got[slotUnsorted].Kind.Valid, "editing a neighbour must not classify an unclassified line")

	// А ТЕПЕРЬ САМА ГАРАНТИЯ, МИМО ПРИЛОЖЕНИЯ. Констрейнты заводились как последний рубеж, а не как
	// дубль валидации, поэтому проверяются прямым SQL.
	//
	// Очевидная запись CHECK (`kind = 'other'`) при kind IS NULL даёт NULL, а NULL MySQL считает
	// выполненным условием — то есть ловит дырку kind='zipper' и пропускает дырку kind IS NULL,
	// которая и есть состояние КАЖДОЙ неклассифицированной строки. Проверяем ОБА плеча.
	_, err = testDB.ExecContext(ctx,
		`UPDATE tech_card_bom_item SET kind = NULL, kind_note = 'теневой вид'
		 WHERE tech_card_id = ? AND line_key = ?`, tcID, slotUnsorted)
	require.Error(t, err, "примечание без вида (kind NULL) должно отвергаться")
	_, err = testDB.ExecContext(ctx,
		`UPDATE tech_card_bom_item SET kind = 'zipper', kind_note = 'теневой вид'
		 WHERE tech_card_id = ? AND line_key = ?`, tcID, slotUnsorted)
	require.Error(t, err, "примечание при виде не-other должно отвергаться")

	// Регистр: REGEXP наследует регистронезависимую коллацию столбца, так что без STRCMP-стража
	// 'ZIPPER' прошёл бы — и не попал бы потом ни в одну группу интерфейса, а первое же сохранение
	// вкладки, которая поля не шлёт, ушло бы в стор и было бы отвергнуто как неизвестный вид на
	// карточке, которую оператор не правил.
	_, err = testDB.ExecContext(ctx,
		`UPDATE tech_card_bom_item SET kind = 'ZIPPER' WHERE tech_card_id = ? AND line_key = ?`,
		tcID, slotUnsorted)
	require.Error(t, err, "вид в другом регистре должен отвергаться")

	// И словарь: значение мимо списка — мусор нигде и значение везде.
	_, err = testDB.ExecContext(ctx,
		`UPDATE tech_card_bom_item SET kind = 'grommet' WHERE tech_card_id = ? AND line_key = ?`,
		tcID, slotUnsorted)
	require.Error(t, err, "вид вне словаря должен отвергаться")

	// СТАРЫЙ КЛИЕНТ НЕ ДОЛЖЕН СТИРАТЬ. Вкладка с бандлом до 0276 этих полей не шлёт вовсе; на
	// проводе это ОТСУТСТВИЕ, а не UNSET, и стор обязан оставить хранимое. Без различения один сейв
	// из открытой вчера вкладки обнулял бы классификацию у ВСЕХ строк карточки — бесследно, потому
	// что полей нет в дайджесте подписи, а NULL неотличим от «ещё не классифицировали».
	stale := make([]entity.TechCardBomItem, len(edited))
	copy(stale, edited)
	for i := range stale {
		stale[i].Kind = sql.NullString{}
		stale[i].KindNote = sql.NullString{}
		stale[i].KindOmitted = true
		stale[i].KindNoteOmitted = true
	}
	require.NoError(t, T.UpdateTechCard(ctx, tcID, mk(stale), lockVersion()))
	afterStale := byKey()
	require.Equal(t, string(entity.BomKindButton), afterStale[slotZip].Kind.String,
		"сейв без поля обязан сохранить вид, а не обнулить его")
	require.Equal(t, string(entity.BomKindHookLoop), afterStale[slotVelcro].Kind.String)
	require.Equal(t, string(entity.BomKindPolybag), afterStale[slotPolybag].Kind.String)
	require.False(t, afterStale[slotUnsorted].Kind.Valid)

	// ПАРА «ВИД ↔ СЕКЦИЯ» ЖИВЁТ В СТОРЕ, а не в схеме, и обязана отказывать с адресным сообщением.
	// Метраж: классифицируется назначением, не видом.
	bad := make([]entity.TechCardBomItem, len(bom))
	copy(bad, bom)
	bad[7].Kind = kind(entity.BomKindZipper)
	err = T.UpdateTechCard(ctx, tcID, mk(bad), lockVersion())
	require.Error(t, err)
	require.Contains(t, err.Error(), "kind applies only to")

	// Этикетки: словарём владеет tech_card_label.label_type, поэтому даже `other` там незаконен.
	copy(bad, bom)
	bad[8].Kind = kind(entity.BomKindOther)
	err = T.UpdateTechCard(ctx, tcID, mk(bad), lockVersion())
	require.Error(t, err)

	// Подходящая секция, но чужая семья: сообщение обязано назвать домашнюю секцию.
	copy(bad, bom)
	bad[3].Kind = kind(entity.BomKindZipper)
	err = T.UpdateTechCard(ctx, tcID, mk(bad), lockVersion())
	require.Error(t, err)
	require.Contains(t, err.Error(), string(entity.BomSectionHardware))

	// Отвергнутые сейвы не должны были примениться наполовину: карточка ровно такая, какой её
	// оставил последний удачный сейв.
	got = byKey()
	require.Equal(t, string(entity.BomKindButton), got[slotZip].Kind.String)
	require.False(t, got[slotFabric].Kind.Valid)
	require.False(t, got[slotLabel].Kind.Valid)
}
