package techcard

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// pieceUpsertParams mirrors the map upsertTechCardPieces builds, so a parameter added to the query and
// forgotten in the map (or the reverse) surfaces here as a bind failure instead of as a 500 on the
// card save path.
func pieceUpsertParams() map[string]any {
	return map[string]any{
		"tech_card_id":         1,
		"name":                 "полочка",
		"line_key":             "01ABCDEF0000000000000001",
		"pieces_per_garment":   2,
		"mirrored":             false,
		"cut_symmetry":         sql.NullString{String: "mirrored", Valid: true},
		"cut_symmetry_omitted": false,
		"ungraded":             false,
		"ungraded_omitted":     false,
		"grainline":            "lengthwise",
		"fused":                false,
		"fusing_mode":          sql.NullString{},
		"fusing_width_mm":      decimal.NullDecimal{},
		"fusing_omitted":       false,
		"callout_number":       sql.NullInt32{},
		"detached":             false,
		"note":                 sql.NullString{},
		"display_order":        0,
		"id":                   7,
	}
}

// The piece upsert grew a guarded column (0275). sqlx parses ':' ANYWHERE in a named query — including
// inside a `--` SQL comment — as a parameter, and a name the args map does not carry fails at BIND
// time, i.e. at request time on the save path, with nothing but a MySQL-backed test to catch it.
// sqlx.Named reproduces both failure modes without a database.
func TestPieceUpsertQueriesBind(t *testing.T) {
	for name, q := range map[string]string{"update": pieceUpdateQuery, "insert": pieceInsertQuery} {
		args, _, err := sqlx.Named(q, pieceUpsertParams())
		if err != nil {
			t.Fatalf("piece %s query does not bind: %v", name, err)
		}
		if strings.Contains(args, ":") {
			t.Fatalf("piece %s query still holds a ':' after binding: %s", name, args)
		}
	}
	if _, args, err := sqlx.Named(pieceReadQuery, map[string]any{"ids": []int{1, 2}}); err != nil || len(args) != 1 {
		t.Fatalf("piece read query does not bind: err=%v args=%d", err, len(args))
	}
}

// The UPDATE must keep the stored value when the payload omitted the field. Losing the IF() is
// invisible to every test that sends the field and catastrophic for the one client that cannot: it
// clears the marking on every piece of the card, and the marking cannot be reconstructed without a
// human holding the patterns.
func TestPieceUpdateGuardsCutSymmetryAgainstAStaleTab(t *testing.T) {
	if !strings.Contains(pieceUpdateQuery, "cut_symmetry=IF(:cut_symmetry_omitted, cut_symmetry, :cut_symmetry)") {
		t.Fatal("the piece UPDATE must carry the stored cut_symmetry forward when the payload omitted it")
	}
	// The INSERT has nothing to carry — a new row has no stored value — so it must NOT be guarded, or
	// it would read a column that does not exist yet for that row.
	if strings.Contains(pieceInsertQuery, "cut_symmetry_omitted") {
		t.Fatal("the piece INSERT must write cut_symmetry directly; there is no stored value to carry")
	}
}

// A column the write stores and the read never loads makes the write-side and read-side digest
// projections permanently disagree, so the sign-off they feed can never match its own stored value
// again — the failure already paid for once on the piece-material line_key. Cheap to assert, so
// assert it.
func TestPieceReadSelectsEveryWrittenColumn(t *testing.T) {
	for _, col := range []string{
		"pieces_per_garment", "mirrored", "cut_symmetry", "grainline", "fused",
		"callout_number", "detached", "note", "line_key",
	} {
		if !strings.Contains(pieceReadQuery, col) {
			t.Errorf("the piece read must SELECT %s: the digest hashes it on the write side", col)
		}
	}
}

// РАЗМЕТКА ДУБЛИРОВАНИЯ (0304) охраняется как и два соседа — но ВЛОЖЕННО в снятую галку, и это не
// стилистика. chk_tcp_fusing_mode двухколоночный: режим законен только у fused-детали. Вкладка со
// старым бандлом режим не шлёт, а галку снять умеет, и голый перенос оставил бы 'strip' рядом с
// fused=0 — то есть уронил бы ВСЁ сохранение карточки в 3819 с именем колонки, которой эта вкладка
// не знает. Потерять это вложение — значит починить один клиент и сломать другой, молча.
func TestPieceUpdateClearsFusingWhenTheBoxIsUnchecked(t *testing.T) {
	for _, want := range []string{
		"fusing_mode=IF(:fused, IF(:fusing_omitted, fusing_mode, :fusing_mode), NULL)",
		"fusing_width_mm=IF(:fused, IF(:fusing_omitted, fusing_width_mm, :fusing_width_mm), NULL)",
	} {
		if !strings.Contains(pieceUpdateQuery, want) {
			t.Fatalf("the piece UPDATE must gate the carried fusing marking on `fused`: missing %s", want)
		}
	}
}

// Ширина и режим ЧИТАЮТСЯ обратно. Колонка, которую запись пишет, а чтение не выбирает, навсегда
// разводит две проекции: подпись CONSTRUCTION хеширует разметку (constructionProjection), и карточка
// перестала бы совпадать со своим же хранимым дайджестом — та самая беда, о которой предупреждает
// шапка pieceReadQuery.
func TestPieceReadSelectsTheFusingColumns(t *testing.T) {
	for _, col := range []string{"fusing_mode", "fusing_width_mm"} {
		if !strings.Contains(pieceReadQuery, col) {
			t.Fatalf("the piece read must SELECT %s — the digest hashes it", col)
		}
	}
}

// СТАРЫЙ КЛИЕНТ НЕ ГАСИТ УЖЕ СНЯТЫЙ ПЕРИМЕТР (0305). Вкладка, которая поля не знает, публикует тот
// же замер без него; будь это «изменением», full-replace записал бы NULL поверх периметров, снятых
// новым клиентом, — молча и на всей ткани, после чего краевое дублирование перестало бы считаться.
//
// Обратное направление ОБЯЗАНО оставаться изменением: иначе периметр не появился бы никогда.
func TestPerimeterComparisonIsAsymmetric(t *testing.T) {
	stored := decimal.NullDecimal{Decimal: decimal.RequireFromString("260"), Valid: true}
	absent := decimal.NullDecimal{}
	other := decimal.NullDecimal{Decimal: decimal.RequireFromString("300"), Valid: true}

	if !perimeterAgrees(stored, absent) {
		t.Error("молчание старого клиента прочиталось как стирание периметра")
	}
	if perimeterAgrees(absent, stored) {
		t.Error("появление периметра прочиталось как «тот же замер» — он не сохранился бы никогда")
	}
	if perimeterAgrees(stored, other) {
		t.Error("другой периметр прочитался как тот же")
	}
	if !perimeterAgrees(stored, stored) {
		t.Error("повтор того же замера прочитался как правка — провенанс переписывался бы зря")
	}
	if !perimeterAgrees(absent, absent) {
		t.Error("два замера без периметра разошлись")
	}
}
