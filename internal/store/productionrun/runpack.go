package productionrun

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// runPackRow — строка прогона плюс имена, которые нужны шапке наряда. Обёртка, а не алиасы в один
// стракт: у прогона своя колонка release_id, и алиас rel.id AS release_id столкнулся бы с ней в
// одном приёмнике.
type runPackRow struct {
	entity.ProductionRun
	StyleNumber   string       `db:"style_number"`
	StyleName     string       `db:"style_name"`
	FactoryName   string       `db:"factory_name"`
	ReleaseNumber int          `db:"release_number"`
	ReleasedAt    sql.NullTime `db:"released_at"`
}

// runPackRunColumns — ЯВНЫЙ список колонок прогона, и явный он ради того, чего в нём нет.
// planned_unit_cost / planned_currency сюда не входят: наряд уезжает на публичный эндпоинт, где нет
// аккаунта, под который можно было бы срезать костинг, поэтому деньги не срезаются на выходе, а не
// читаются на входе (тот же довод, что в techcard.GetPatternViewerManifest). SELECT * здесь был бы
// не «удобнее», а тем, что тащит цену в процесс, который её печатает.
const runPackRunColumns = `
	r.id, r.tech_card_id, r.release_id, r.status, r.started_at, r.received_at,
	r.marker_efficiency_pct, r.marker_notes, r.actual_wastage_percent, r.notes,
	r.planned_start_at, r.promised_at, r.supplier_id, r.lock_version, r.created_at, r.updated_at`

// GetRunPack — узкое чтение под манифест наряда (GET /api/rp/{token}): шапка прогона, стиль,
// фабрика, номер релиза и плановый грид с именами колорвеев и размеров.
//
// Сознательно НЕ GetProductionRun: тот дочитывает статьи затрат, движения материала, приёмки,
// журнал событий и сверку — деньги и историю, которым в наряде делать нечего, четырьмя лишними
// запросами на каждое сканирование QR. Сама СТРОКА прогона читается полностью (кроме денежных
// колонок), поэтому любое скалярное поле, которое проекция кат-листа может прочитать, — настоящее;
// не загружены ровно те коллекции, которые проекции без денег читать нечего.
//
// ПОРЯДОК ДВУХ ЗАПРОСОВ ЗДЕСЬ — ЭТО РЕШЕНИЕ, А НЕ СЛУЧАЙНОСТЬ. Шапка (вместе с lock_version)
// читается ПЕРВОЙ, строки — второй, вне одной транзакции. Правка прогона между ними даёт документ,
// у которого напечатанная версия СТАРШЕ показанных количеств, и вьюер, сравнивая напечатанное с
// текущим, скажет «план изменился после печати» — ложная тревога в безопасную сторону. Обратный
// порядок дал бы версию НОВЕЕ количеств, то есть «всё актуально» поверх устаревшего грида, а это
// уже партия, раскроенная не по тем числам.
//
// Релиз джойнится по ДВУМ условиям (id и tech_card_id): release_id — обычная колонка, а не
// гарантия принадлежности, и «Rev.3» чужой карты на шапке наряда выглядел бы совершенно
// нормально ровно до цеха.
//
// sql.ErrNoRows, когда прогона нет, — обработчик превращает это в тот же голый 404, что и всё
// остальное.
func (s *Store) GetRunPack(ctx context.Context, runID int) (*entity.RunPack, error) {
	row, err := storeutil.QueryNamedOne[runPackRow](ctx, s.DB, `
		SELECT `+runPackRunColumns+`,
		       COALESCE(tc.style_number, '') AS style_number,
		       COALESCE(tc.name, '') AS style_name,
		       COALESCE(sup.name, '') AS factory_name,
		       COALESCE(rel.release_number, 0) AS release_number,
		       rel.created_at AS released_at
		FROM production_run r
		JOIN tech_card tc ON tc.id = r.tech_card_id
		LEFT JOIN supplier sup ON sup.id = r.supplier_id
		LEFT JOIN tech_card_release rel ON rel.id = r.release_id AND rel.tech_card_id = r.tech_card_id
		WHERE r.id = :id`, map[string]any{"id": runID})
	if err != nil {
		// sql.ErrNoRows проходит без обёртки: обработчик отличает «прогона нет» от сбоя запроса
		// через errors.Is, и обёртка сломала бы ровно эту ветку.
		return nil, err
	}

	// Линия читается ЦЕЛИКОМ, включая received_qty/defect_qty, хотя наряд печатает только план:
	// эти строки едут в entity.ProductionRun как вход проекции кат-листа, и проекция, которой
	// подсунули наполовину заполненную сущность, ошибается молча. Печатать факт приёмки — решение
	// манифеста (он его не печатает), а не чтения.
	lines, err := storeutil.QueryListNamed[entity.RunPackLine](ctx, s.DB, `
		SELECT pl.id, COALESCE(pl.line_key, '') AS line_key, pl.product_id, pl.output_variant_id,
		       COALESCE(pl.size_id, 0) AS size_id, pl.planned_qty,
		       pl.received_qty, pl.defect_qty,
		       COALESCE(sz.name, '') AS size_name,
		       COALESCE(NULLIF(p.dev_name, ''), NULLIF(p.color, ''), '') AS colorway_name,
		       COALESCE(oc.name, '') AS output_variant_name
		FROM production_run_line pl
		LEFT JOIN size sz ON sz.id = pl.size_id
		LEFT JOIN product p ON p.id = pl.product_id
		LEFT JOIN tech_card_output_variant ov ON ov.id = pl.output_variant_id
		LEFT JOIN color oc ON oc.code = ov.color_code
		WHERE pl.run_id = :id
		ORDER BY pl.product_id IS NOT NULL, pl.product_id, pl.output_variant_id, pl.size_id`,
		map[string]any{"id": runID})
	if err != nil {
		return nil, fmt.Errorf("load run pack lines of run %d: %w", runID, err)
	}

	// LEFT JOIN везде, где имя приезжает из словаря, и COALESCE поверх: линия может ссылаться на
	// архивный продукт или на размер, выпавший из градации карты, и такая линия обязана попасть в
	// наряд с пустым именем, а не исчезнуть из него вместе со своим плановым количеством.
	run := row.ProductionRun
	run.Lines = make([]entity.ProductionRunLine, 0, len(lines))
	for i := range lines {
		run.Lines = append(run.Lines, lines[i].ProductionRunLine)
	}

	return &entity.RunPack{
		Run:           run,
		Lines:         lines,
		StyleNumber:   row.StyleNumber,
		StyleName:     row.StyleName,
		FactoryName:   row.FactoryName,
		ReleaseNumber: row.ReleaseNumber,
		ReleasedAt:    row.ReleasedAt,
	}, nil
}

// GetRunPackMarkerSizes читает СОСТАВ набора раскладок одним запросом — размеры, которые лежат на
// настиле, для его краткой карточки в наряде. Имя размера берётся из словаря тем же LEFT JOIN и по
// той же причине, что и в гриде.
//
// Раскладка без строк состава — это не ошибка, а строка периода пересборки схемы (0273): вызывающий
// получает пустой список и печатает настил без разбивки, а не выдумывает состав из одного размера
// (легаси-подстановка живёт в entity.CompositionOrLegacy и должна остаться в одном месте).
func (s *Store) GetRunPackMarkerSizes(ctx context.Context, markerIDs []int) (map[int][]entity.RunPackMarkerSize, error) {
	out := make(map[int][]entity.RunPackMarkerSize, len(markerIDs))
	if len(markerIDs) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[entity.RunPackMarkerSize](ctx, s.DB, `
		SELECT ms.marker_id, ms.size_id, ms.quantity, COALESCE(sz.name, '') AS size_name
		FROM tech_card_marker_size ms
		LEFT JOIN size sz ON sz.id = ms.size_id
		WHERE ms.marker_id IN (:ids)
		ORDER BY ms.marker_id, ms.size_id`, map[string]any{"ids": markerIDs})
	if err != nil {
		return nil, fmt.Errorf("load run pack marker composition: %w", err)
	}
	for i := range rows {
		out[rows[i].MarkerId] = append(out[rows[i].MarkerId], rows[i])
	}
	return out, nil
}
