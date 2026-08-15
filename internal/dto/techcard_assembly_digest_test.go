package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// Отпечаток CONSTRUCTION и узлы сборки (0307).
//
// Тут проверяются ровно два утверждения, и оба — про то, чего фича НЕ должна сделать. Первое:
// выкатка не протухает ни одной действующей подписи. Второе: подпись, поставленная на записи,
// совпадает с отпечатком следующего чтения — иначе размеченная карточка рождается «изменённой
// после подписи» и остаётся такой навсегда.

func asmDigestCard(ops ...entity.TechCardOperation) *entity.TechCardInsert {
	return &entity.TechCardInsert{
		Construction: &entity.TechCardConstruction{HemFinish: ns("подгибка 2 см")},
		Operations:   ops,
		Pieces: []entity.TechCardPiece{
			{LineKey: "FR", Name: "полочка", PiecesPerGarment: 1},
			{LineKey: "BK", Name: "спинка", PiecesPerGarment: 1},
		},
	}
}

func asmPlainOp(keys ...string) entity.TechCardOperation {
	return entity.TechCardOperation{
		OperationNumber: ni32(10), OperationType: "machine", Zone: "closure",
		PieceLineKeys: keys,
	}
}

// TestAssemblyDigestUnchangedForPieceOnlyCard — САМОЕ ВАЖНОЕ утверждение всей фичи для базы.
//
// Стор после 0307 заполняет AssemblyInputs на КАЖДОЙ операции, у которой есть связи с деталями,
// то есть на каждой сегодняшней карточке. Если бы предикат хвоста считал вход-деталь сборочным
// фактом, отпечаток каждой такой карточки сдвинулся бы в момент выкатки и все утверждённые
// подписи CONSTRUCTION разом стали бы «устаревшими» — ни за что.
//
// Поэтому: карточка, прочитанная СТАРЫМ кодом (AssemblyInputs пуст), и та же карточка,
// прочитанная НОВЫМ (AssemblyInputs заполнен деталями), обязаны дать один отпечаток.
func TestAssemblyDigestUnchangedForPieceOnlyCard(t *testing.T) {
	asRead := asmPlainOp("FR", "BK")
	asRead.AssemblyInputs = []entity.OperationInput{
		{Kind: entity.AssemblyInputPiece, Key: "FR"},
		{Kind: entity.AssemblyInputPiece, Key: "BK"},
	}
	asRead.InputKeys = []string{"FR", "BK"}

	before := TechCardSectionDigests(asmDigestCard(asmPlainOp("FR", "BK")))
	after := TechCardSectionDigests(asmDigestCard(asRead))

	require.Equal(t, before[entity.SignoffConstruction], after[entity.SignoffConstruction],
		"вход-деталь не факт сборки: хвост не должен появляться, иначе выкатка объявит устаревшими все подписанные карточки")
}

// TestAssemblyDigestAppearsOnlyWithUnits — хвост появляется ровно тогда, когда появляется узел, и
// это честно: подписанное содержание действительно двинулось.
func TestAssemblyDigestAppearsOnlyWithUnits(t *testing.T) {
	plain := asmDigestCard(asmPlainOp("FR", "BK"))

	marked := asmPlainOp("FR", "BK")
	marked.OutputUnitKey = sql.NullString{String: "SHELL", Valid: true}
	marked.AssemblyInputs = []entity.OperationInput{
		{Kind: entity.AssemblyInputPiece, Key: "FR"},
		{Kind: entity.AssemblyInputPiece, Key: "BK"},
	}

	require.NotEqual(t,
		TechCardSectionDigests(plain)[entity.SignoffConstruction],
		TechCardSectionDigests(asmDigestCard(marked))[entity.SignoffConstruction],
		"разметка узла — содержательная правка, отпечаток обязан двинуться")
}

// TestAssemblyDigestIgnoresUnitName — имя узла в отпечаток не входит: оно разрешается по первому
// производителю и фактом цеха не является. Хешируй его, и невидимая правка на поглощающем шаге
// протухала бы подпись.
func TestAssemblyDigestIgnoresUnitName(t *testing.T) {
	mk := func(name string) *entity.TechCardInsert {
		op := asmPlainOp("FR", "BK")
		op.OutputUnitKey = sql.NullString{String: "SHELL", Valid: true}
		op.OutputUnitName = sql.NullString{String: name, Valid: name != ""}
		op.AssemblyInputs = []entity.OperationInput{
			{Kind: entity.AssemblyInputPiece, Key: "FR"},
			{Kind: entity.AssemblyInputPiece, Key: "BK"},
		}
		return asmDigestCard(op)
	}
	require.Equal(t,
		TechCardSectionDigests(mk("корпус"))[entity.SignoffConstruction],
		TechCardSectionDigests(mk("корпус изделия"))[entity.SignoffConstruction],
		"имя узла не должно влиять на отпечаток")
}

// TestAssemblyDigestSeesInterleave — весь упорядоченный union хешируется. Первая редакция плана
// исключала интерлив, и семантика подписи выходила непоследовательной: перестановка двух ДЕТАЛЕЙ
// отпечаток меняла (позиция 4 — упорядоченный список), а перестановка детали и узла — нет.
func TestAssemblyDigestSeesInterleave(t *testing.T) {
	mk := func(inputs ...entity.OperationInput) *entity.TechCardInsert {
		op := asmPlainOp()
		op.OutputUnitKey = sql.NullString{String: "GARMENT", Valid: true}
		op.AssemblyInputs = inputs
		for _, in := range inputs {
			if in.Kind == entity.AssemblyInputPiece {
				op.PieceLineKeys = append(op.PieceLineKeys, in.Key)
			}
		}
		return asmDigestCard(op)
	}
	a := mk(
		entity.OperationInput{Kind: entity.AssemblyInputUnit, Key: "SHELL"},
		entity.OperationInput{Kind: entity.AssemblyInputPiece, Key: "FR"},
	)
	b := mk(
		entity.OperationInput{Kind: entity.AssemblyInputPiece, Key: "FR"},
		entity.OperationInput{Kind: entity.AssemblyInputUnit, Key: "SHELL"},
	)
	require.NotEqual(t,
		TechCardSectionDigests(a)[entity.SignoffConstruction],
		TechCardSectionDigests(b)[entity.SignoffConstruction],
		"порядок объединения — часть подписанного содержания")
}

// TestAssemblyDigestWriteReadRoundTrip — подпись, поставленная на ЗАПИСИ, обязана совпасть с
// отпечатком того, что вернёт ЧТЕНИЕ.
//
// Это тот самый класс дефекта, ради которого канонизация переехала из стора в конвертер, и
// отдельно — ловушка nil против пустого среза: json.Marshal их различает, и шаг, записанный как
// [], но прочитанный как null, дал бы разные отпечатки навсегда.
func TestAssemblyDigestWriteReadRoundTrip(t *testing.T) {
	// Форма ЗАПИСИ: то, что оставляет после себя canonicalizeAssembly.
	writeOps := []entity.TechCardOperation{
		{OperationNumber: ni32(10), OperationType: "machine", Zone: "closure",
			InputKeys: []string{"FR", "BK"}, OutputUnitKey: sql.NullString{String: "SHELL", Valid: true}},
		// Шаг БЕЗ входов — здесь и живёт ловушка nil/[].
		{OperationNumber: ni32(20), OperationType: "machine", Zone: "closure"},
	}
	pieces := []entity.TechCardPiece{
		{LineKey: "FR", Name: "полочка", PiecesPerGarment: 1},
		{LineKey: "BK", Name: "спинка", PiecesPerGarment: 1},
	}
	require.Nil(t, canonicalizeAssembly(writeOps, pieces))

	// Форма ЧТЕНИЯ: то, что собирает стор из строк tech_card_operation_input.
	readOps := []entity.TechCardOperation{
		{OperationNumber: ni32(10), OperationType: "machine", Zone: "closure",
			OutputUnitKey: sql.NullString{String: "SHELL", Valid: true},
			InputKeys:     []string{"FR", "BK"},
			PieceLineKeys: []string{"FR", "BK"},
			AssemblyInputs: []entity.OperationInput{
				{Kind: entity.AssemblyInputPiece, Key: "FR"},
				{Kind: entity.AssemblyInputPiece, Key: "BK"},
			}},
		{OperationNumber: ni32(20), OperationType: "machine", Zone: "closure"},
	}

	write := &entity.TechCardInsert{
		Construction: &entity.TechCardConstruction{HemFinish: ns("подгибка 2 см")},
		Operations:   writeOps, Pieces: pieces,
	}
	read := &entity.TechCardInsert{
		Construction: &entity.TechCardConstruction{HemFinish: ns("подгибка 2 см")},
		Operations:   readOps, Pieces: pieces,
	}
	require.Equal(t,
		TechCardSectionDigests(write)[entity.SignoffConstruction],
		TechCardSectionDigests(read)[entity.SignoffConstruction],
		"отпечаток записи и отпечаток чтения обязаны совпасть, иначе карточка рождается протухшей")
}
