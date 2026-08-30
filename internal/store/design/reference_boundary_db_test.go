package design_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ПРОБЫ ГРАНИЦЫ РОЛИ РЕФЕРЕНСА (R-10) И АВТО-РОЛЕЙ СПЛИТА (R-11).
//
// R-10: положительный сторож SetReferenceRole («медиа лежит в tech_card_media ЭТОЙ карточки»)
// ложно отказывал на двух законных жестах — картинке входа, которую клиент кладёт только в
// НЕСОХРАНЁННУЮ форму (tech_card_media переписывается лишь сейвом всей карточки), и кропе сплита,
// который живёт в design_picture и в tech_card_media не попадает никогда. Лечение — отрицательная
// граница refuseForeignMedia («не принадлежит ДРУГОЙ карточке»), и она обязана остаться УЗКОЙ:
// первые две пробы держат «пройти должно», третья — «чужая карточка получает отказ по-прежнему».
// Убрать третью — починка молча открывает промпт чужим картинкам, поэтому она здесь обязательная,
// а не декоративная.
//
// R-11: SplitPicture сам заводит design_reference с ролью из view_key кадра (решение владельца:
// «детали сплита добавляются в промпт помеченными ролью вида»; словарь ролей и словарь view_key —
// один и тот же, IsDesignGhostView проверяет оба). Кадр БЕЗ view_key роли не получает, повтор
// сплита второго набора ролей не рождает.
//
// ОБВЯЗКА ОБЩАЯ с wave2_db_test.go (probeRepository / probeCard / probeMedia): без CI=1 всё
// пропускается ДО открытия соединения, имя базы, похожее на продовое, отвергается отдельно.

// ─────────────────────── R-10: что обязано ПРОЙТИ ───────────────────────

// Свежезагруженный файл не принадлежит ещё НИ ОДНОЙ карточке — контракт ImportDesignVector говорит
// это про source_media_id дословно, и вход генерации до сейва карточки устроен так же. Роль на нём
// обязана ставиться: между «добавил картинку» и «нажал Save» выбор роли — законный жест, а не
// нарушение границы.
func TestDesignDBReferenceRoleAcceptsFreshUnheldMedia(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	media := probeMedia(t, raw)

	ref, err := rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: card, MediaId: media, Role: entity.DesignViewFront, Ordinal: 1, Actor: "probe",
	})
	require.NoError(t, err,
		"ничейное (свежезагруженное) медиа обязано принимать роль ДО сейва карточки — R-10")
	require.NotNil(t, ref)
	require.Equal(t, entity.DesignViewFront, ref.Role)
	require.Equal(t, media, ref.MediaId)
}

// Кроп сплита живёт в design_picture — второй держатель границы. Кадр здесь режется БЕЗ view_key,
// чтобы авто-роль R-11 не родилась и роль ставил именно ручной глагол (путь клиентской половины
// R-11: «в промпт» с плитки).
func TestDesignDBReferenceRoleAcceptsOwnBandCrop(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	parentMedia := probeMedia(t, raw)
	cropMedia := probeMedia(t, raw)

	batch, err := rep.Design().RegisterBatch(ctx, entity.DesignBatchRegister{
		TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{{MediaId: parentMedia, Kind: entity.DesignPictureKindFlat}},
	})
	require.NoError(t, err)
	require.Len(t, batch.Pictures, 1)

	crops, err := rep.Design().SplitPicture(ctx, entity.DesignSplitRequest{
		PictureId: batch.Pictures[0].Id, ClientRequestId: uuid.NewString(), Actor: "probe",
		Frames: []entity.DesignSplitFrame{{MediaId: cropMedia}},
	})
	require.NoError(t, err)
	require.Len(t, crops, 1)

	ref, err := rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: card, MediaId: cropMedia, Role: entity.DesignViewDetail, Ordinal: 2, Actor: "probe",
	})
	require.NoError(t, err,
		"кроп сплита держит design_picture этой карточки и обязан принимать роль — R-10/R-11")
	require.NotNil(t, ref)
	require.Equal(t, entity.DesignViewDetail, ref.Role)
}

// ─────────────────────── R-10: что обязано ОТКАЗЫВАТЬ по-прежнему ───────────────────────

// ОБЯЗАТЕЛЬНАЯ ПРОБА УЗОСТИ. Ослабление границы законно ровно до тех пор, пока медиа, которое
// держит ЧУЖАЯ карточка (любым из двух держателей) и не держит эта, получает отказ: иначе роль
// скармливает модели чужую картинку. Мутация «убрать refuseForeignMedia вовсе» краснит именно её.
func TestDesignDBReferenceRoleStillRefusesForeignCard(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	mine := probeCard(t, raw)
	other := probeCard(t, raw)

	// Держатель 1: design_picture чужой карточки.
	bandMedia := probeMedia(t, raw)
	_, err := rep.Design().RegisterBatch(ctx, entity.DesignBatchRegister{
		TechCardId: other, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{{MediaId: bandMedia, Kind: entity.DesignPictureKindFlat}},
	})
	require.NoError(t, err)
	_, err = rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: mine, MediaId: bandMedia, Role: entity.DesignViewFront, Ordinal: 1, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignForeignMedia,
		"картинка ПОЛОСЫ чужой карточки обязана получать отказ")

	// Держатель 2: tech_card_media чужой карточки.
	heldMedia := probeMedia(t, raw)
	_, err = raw.Exec(`INSERT INTO tech_card_media (tech_card_id, media_id, kind, display_order)
		VALUES (?, ?, 'preview', 0)`, other, heldMedia)
	require.NoError(t, err)
	// Снимается ПЕРВЫМ (LIFO), чтобы удаление media в чистке probeMedia не упёрлось в FK.
	t.Cleanup(func() { _, _ = raw.Exec(`DELETE FROM tech_card_media WHERE media_id = ?`, heldMedia) })
	_, err = rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: mine, MediaId: heldMedia, Role: entity.DesignViewFront, Ordinal: 1, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignForeignMedia,
		"медиа из tech_card_media чужой карточки обязано получать отказ")

	// Контроль узости с другой стороны: та же картинка СВОЕЙ карточке роль ставит — отказ выше
	// был про принадлежность, а не про сам файл.
	ref, err := rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: other, MediaId: bandMedia, Role: entity.DesignViewFront, Ordinal: 1, Actor: "probe",
	})
	require.NoError(t, err)
	require.NotNil(t, ref)
}

// ─────────────────────── R-11: авто-роли сплита ───────────────────────

type promptRefRow struct {
	Media   int
	Role    string
	Ordinal int
	SetBy   string
}

// readRefs читает строки design_reference карточки в порядке промпта — тем же ORDER BY, каким их
// читает сборка входов прогона: сравнение по этим срезам судит ровно то, что увидит модель.
func readRefs(t *testing.T, raw *sql.DB, card int) []promptRefRow {
	t.Helper()
	rows, err := raw.Query(
		`SELECT media_id, role, ordinal, set_by FROM design_reference WHERE tech_card_id = ? ORDER BY ordinal, id`,
		card)
	require.NoError(t, err)
	defer rows.Close()
	var out []promptRefRow
	for rows.Next() {
		var r promptRefRow
		require.NoError(t, rows.Scan(&r.Media, &r.Role, &r.Ordinal, &r.SetBy))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// Кадры с view_key рождают роль этого вида, кадр без view_key — НЕ рождает (придуманная роль
// соврала бы модели), а ординалы встают ХВОСТОМ за уже стоящими референсами мудборда: промпт
// читается ORDER BY ordinal, и кроп с ordinal 0 пролез бы вперёд порядка, назначенного человеком.
func TestDesignDBSplitStampsPromptRolesFromViewKeys(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	parentMedia := probeMedia(t, raw)
	m1, m2, m3 := probeMedia(t, raw), probeMedia(t, raw), probeMedia(t, raw)

	// Уже стоящий референс мудборда с ordinal 5 — авто-роли обязаны встать ЗА ним.
	moodMedia := probeMedia(t, raw)
	_, err := raw.Exec(`INSERT INTO design_reference (tech_card_id, media_id, role, ordinal, set_by, set_at)
		VALUES (?, ?, 'front', 5, 'probe-mood', UTC_TIMESTAMP(6))`, card, moodMedia)
	require.NoError(t, err)

	batch, err := rep.Design().RegisterBatch(ctx, entity.DesignBatchRegister{
		TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{{MediaId: parentMedia, Kind: entity.DesignPictureKindFlat}},
	})
	require.NoError(t, err)

	crops, err := rep.Design().SplitPicture(ctx, entity.DesignSplitRequest{
		PictureId: batch.Pictures[0].Id, ClientRequestId: uuid.NewString(), Actor: "probe-split",
		Frames: []entity.DesignSplitFrame{
			{MediaId: m1, ViewKey: entity.DesignViewFront},
			{MediaId: m2}, // вид не объявлен — роли быть не должно
			{MediaId: m3, ViewKey: entity.DesignViewDetail},
		},
	})
	require.NoError(t, err)
	require.Len(t, crops, 3)

	refs := readRefs(t, raw, card)
	require.Len(t, refs, 3, "мудборд + два кадра с видом; кадр без вида роли не получает")
	require.Equal(t, promptRefRow{Media: moodMedia, Role: "front", Ordinal: 5, SetBy: "probe-mood"}, refs[0])
	require.Equal(t, promptRefRow{Media: m1, Role: entity.DesignViewFront, Ordinal: 6, SetBy: "probe-split"}, refs[1])
	require.Equal(t, promptRefRow{Media: m3, Role: entity.DesignViewDetail, Ordinal: 7, SetBy: "probe-split"}, refs[2])
	for _, r := range refs {
		require.NotEqual(t, m2, r.Media, "кадр без view_key не должен получать роль — её никто не называл")
	}
}

// Повтор сплита (сетевой ретрай хендлера приезжает с НОВЫМИ медиа перезалитых кропов) обязан
// вернуть ТЕ ЖЕ картинки и НЕ родить второй набор ролей: срез повтора стоит выше штампа ролей,
// в той же транзакции.
func TestDesignDBSplitRetryDoesNotDoubleRoles(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	parentMedia := probeMedia(t, raw)
	m1 := probeMedia(t, raw)

	batch, err := rep.Design().RegisterBatch(ctx, entity.DesignBatchRegister{
		TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{{MediaId: parentMedia, Kind: entity.DesignPictureKindFlat}},
	})
	require.NoError(t, err)
	parent := batch.Pictures[0].Id

	first, err := rep.Design().SplitPicture(ctx, entity.DesignSplitRequest{
		PictureId: parent, ClientRequestId: uuid.NewString(), Actor: "probe",
		Frames: []entity.DesignSplitFrame{{MediaId: m1, ViewKey: entity.DesignViewFront}},
	})
	require.NoError(t, err)
	require.Len(t, first, 1)
	before := readRefs(t, raw, card)
	require.Len(t, before, 1)

	// Ретрай: тот же родитель, но кроп перезалит под новым media id.
	m2 := probeMedia(t, raw)
	second, err := rep.Design().SplitPicture(ctx, entity.DesignSplitRequest{
		PictureId: parent, ClientRequestId: uuid.NewString(), Actor: "probe",
		Frames: []entity.DesignSplitFrame{{MediaId: m2, ViewKey: entity.DesignViewFront}},
	})
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Equal(t, first[0].Id, second[0].Id, "повтор обязан вернуть ТЕ ЖЕ строки, а не второй набор")
	require.Equal(t, first[0].MediaId, second[0].MediaId)

	after := readRefs(t, raw, card)
	require.Equal(t, before, after, "повтор сплита не имеет права ни добавить роль, ни сдвинуть ординал")
	for _, r := range after {
		require.NotEqual(t, m2, r.Media, "медиа непринятого ретрая не должно получать роль")
	}
}
