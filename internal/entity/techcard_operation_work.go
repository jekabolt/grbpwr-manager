package entity

import (
	"database/sql"
	"time"
)

// КАТАЛОГ РАБОТ (видов операций) — СЕРВЕРНЫЕ ДАННЫЕ, А НЕ КОНТРАКТ.
//
// До миграции 0329 вид операции существовал ровно одним способом: списком из полусотни строк в
// клиентском `operation-kinds.ts`, который НИГДЕ НЕ ХРАНИЛСЯ — экран каждый раз заново выводил вид
// из пары (глагол, машинка). Сервер поэтому не мог ни проверить «такая работа существует», ни
// отдать поиск по русскому слову технолога, ни повесить на работу правило поля.
//
// ЭТИ ТИПЫ НЕ НЕСУТ САМИХ ДАННЫХ. Каталог живёт в таблицах `operation_work*` и правится ТОЛЬКО
// миграциями; здесь — форма строки и два закрытых словаря (стадия, режим машинки), которые SQL
// закрыть не может: `ADD CONSTRAINT CHECK` копирует таблицу целиком, а заводить пятую и шестую
// справочные таблицы ради двух словариков по восемь и три члена несоразмерно. Их стережёт
// guard-тест над текстом миграции (internal/store/migrationlint).
//
// ЧТО ЗДЕСЬ НЕИЗМЕНЯЕМО: пара token→verb. Токен уезжает в проекцию дайджеста строки шага, verb
// входит в правило когерентности — правка любого из них задним числом раздваивает отпечаток уже
// подписанной карточки. label / stage / sort / синонимы — представление, правятся дёшево и в
// дайджест не входят никогда.

// OperationWork is one row of the work catalog: WHAT the step does, in the word a technologist
// says at the machine. `Machines` and `Syn` are filled by the catalog reader from the two child
// tables — they are not columns of `operation_work` and carry no db tag.
type OperationWork struct {
	Token          string         `db:"token"`
	Verb           string         `db:"verb"`
	Stage          string         `db:"stage"`
	Label          string         `db:"label"`
	MachineMode    string         `db:"machine_mode"`
	DefaultMachine sql.NullString `db:"default_machine"`
	Sort           int            `db:"sort"`
	// RetiredAt снимает пункт с предложения, НЕ снимая его с чтения: строка шага, уже несущая этот
	// токен, обязана открываться и сохраняться. Удаления работы не бывает вовсе.
	RetiredAt sql.NullTime `db:"retired_at"`

	Machines []string `db:"-"`
	Syn      []string `db:"-"`
}

// Retired reports whether the work is withdrawn from the picker (but still readable).
func (w OperationWork) Retired() bool { return w.RetiredAt.Valid }

// OperationWorkDefault is a GLOBAL default of one work-property field — the only catalog table
// written at runtime (the «remember as default» gesture), and therefore the only one that is not
// identity. Machine and pressing settings are deliberately NOT storable here: they already have an
// inheritance ladder (equipment profiles, 0306), and two mechanisms on one field would answer one
// question twice.
type OperationWorkDefault struct {
	WorkToken string    `db:"work_token"`
	Field     string    `db:"field"`
	Value     string    `db:"value"`
	UpdatedAt time.Time `db:"updated_at"`
}

// Режим вопроса «на чём» у работы.
//
//	fixed — машинка СЛЕДУЕТ из работы («московский» уже содержит прямострочку, и она не
//	        произносится вовсе); default_machine заполнен, в operation_work_machine ровно одна строка
//	ask   — работа законно живёт на нескольких машинках (отстрочка), вопрос стоит рядом с именем
//	        пункта; default_machine — одна из перечисленных, строк две и больше
//	none  — ось «на чём» у этого глагола не машинная (ВТО, фурнитура, финиш); default_machine NULL,
//	        строк в operation_work_machine нет
const (
	OperationWorkMachineModeFixed = "fixed"
	OperationWorkMachineModeAsk   = "ask"
	OperationWorkMachineModeNone  = "none"
)

// OperationWorkMachineModes is THE vocabulary of machine modes, in reading order.
var OperationWorkMachineModes = []string{
	OperationWorkMachineModeFixed,
	OperationWorkMachineModeAsk,
	OperationWorkMachineModeNone,
}

// OperationWorkStageTokens is THE closed vocabulary of catalog groups — ВОСЕМЬ СТАДИЙ РАБОТЫ, НЕ
// СЕМЕЙСТВ ЖЕЛЕЗА. Прежние клиентские шапки («cycle machines & embroidery», «automats») называли
// станок, и технолог, думающий «подогнуть», обязан был знать, на каком станке это делается, чтобы
// найти своё слово. Порядок — порядок, которым идёт работа над изделием.
var OperationWorkStageTokens = []string{
	"join_seam", "edges_hems", "closures", "hardware",
	"pressing", "print_decorate", "finishing", "other",
}

// ValidOperationWorkStages / ValidOperationWorkMachineModes are the sets derived from the slices
// above — derived, never retyped, so a token added to one is not missing from the other.
var (
	ValidOperationWorkStages       = stringTokenSet(OperationWorkStageTokens)
	ValidOperationWorkMachineModes = stringTokenSet(OperationWorkMachineModes)
)

func stringTokenSet(tokens []string) map[string]bool {
	m := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		m[t] = true
	}
	return m
}
