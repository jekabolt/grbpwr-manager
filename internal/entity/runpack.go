package entity

import (
	"database/sql"
	"time"
)

// НАРЯД НА ПАРТИЮ (run pack) — публичный документ прогона: то, что цех открывает по QR с бумаги,
// без JWT и без сгенерённого клиента (GET /api/rp/{token}, internal/runpackaccess). Здесь живут
// ТОЛЬКО его строка доступа и узкое чтение под манифест; сам манифест собирается в
// internal/runpackaccess из этих данных плюс проекции кат-листа и материального плана.

// ProductionRunPackAccess — строка доступа к наряду (production_run_pack_access, 0293), прогонный
// близнец TechCardPatternViewerAccess. Та же механика epoch/revoke/expiry, но идентичность — id
// ПРОГОНА: токен скоупа 'r' резолвится только против этой таблицы и никогда против карточной или
// объектной (числовые диапазоны пересекаются, а означают разное).
type ProductionRunPackAccess struct {
	RunId        int          `db:"run_id"`
	Epoch        int          `db:"epoch"`
	ExpiresAt    sql.NullTime `db:"expires_at"`
	RevokedAt    sql.NullTime `db:"revoked_at"`
	LastAccessAt sql.NullTime `db:"last_access_at"`
	AccessCount  int64        `db:"access_count"`
	CreatedAt    time.Time    `db:"created_at"`
}

// RunPackLine — одна клетка планового грида прогона (колорвей × размер) вместе с именами, которые
// нужны бумаге. Имена приезжают из СЛОВАРЕЙ джойном, а не из тех-карты: строка прогона может
// ссылаться на архивный колорвей или на размер, выпавший из градации карты, и поиск имени в карте
// вернул бы пустоту ровно там, где цеху надо прочитать, что именно он кроит.
type RunPackLine struct {
	ProductionRunLine
	ColorwayName      string `db:"colorway_name"`
	OutputVariantName string `db:"output_variant_name"`
	SizeName          string `db:"size_name"`
}

// RunPack — узкое чтение под манифест наряда: шапка прогона, названия стиля/фабрики/ревизии и
// плановый грид с именами.
//
// ДЕНЕГ ЗДЕСЬ НЕТ, И ЭТО СВОЙСТВО ЧТЕНИЯ, А НЕ СТРИПА НА ВЫХОДЕ. Run заполняется ЯВНЫМ списком
// колонок, в котором нет ни planned_unit_cost/planned_currency, ни статей затрат, ни движений
// материала; tech_card_release читается без unit_cost/currency. Публичный эндпоинт не имеет RBAC,
// которым можно было бы срезать костинг постфактум, поэтому безопасная форма — не читать его вовсе:
// то, что не прочитано, невозможно случайно напечатать (тот же довод, что в
// techcard.GetPatternViewerManifest).
type RunPack struct {
	// Run — строка прогона и её плановые линии, и БОЛЬШЕ НИЧЕГО: ни Costs, ни MaterialMovements, ни
	// Receipts, ни Events, ни Recon. Это вход проекции кат-листа (dto.ComputeProductionRunCutPlan),
	// которая по контракту денег не смотрит; незагруженные коллекции — та же гарантия, выраженная
	// структурно.
	Run ProductionRun
	// Lines — те же линии, что и Run.Lines, но с именами колорвея/цвета/размера для печати.
	Lines []RunPackLine

	StyleNumber string
	StyleName   string
	// FactoryName — supplier.name прогона; пусто, когда фабрика не назначена.
	FactoryName string
	// ReleaseNumber — «Rev.N», по которому кроят; 0 = прогон не привязан к релизу (живая карта).
	// ReleasedAt — дата этого релиза, нужна только чтобы собрать TechCardReleaseMeta для проекции.
	ReleaseNumber int
	ReleasedAt    sql.NullTime
}

// RunPackMarkerSize — одна строка СОСТАВА раскладки (tech_card_marker_size): сколько изделий этого
// размера лежит на раскладке в ОДНОМ слое. Умножать на слои здесь нельзя и не нужно: выход настила
// считает dto (LayCoverage) с учётом режима настилания, и вторая арифметика того же числа рано или
// поздно разойдётся с первой.
type RunPackMarkerSize struct {
	MarkerId int    `db:"marker_id"`
	SizeId   int    `db:"size_id"`
	SizeName string `db:"size_name"`
	Quantity int    `db:"quantity"`
}
