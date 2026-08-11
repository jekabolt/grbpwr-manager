package entity

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// ПЛОЩАДИ ДЕТАЛЕЙ КРОЯ (Ф0, 0297) — разобранная геометрия, положенная на сервер, чтобы норму
// расхода можно было ВЫВЕСТИ, а не требовать её ввода.
//
// ОТКУДА ВЗЯЛАСЬ ЗАДАЧА. Карточка может быть заполнена целиком — спецификация с ценами, разобранные
// выкройки, назначение каждой детали на ткань в рецепте — и всё равно стоить ноль: деньги считает
// только строка рецепта с явно вписанной нормой, а строку, привязанную к детали, расчёт пропускает
// (T8, «расход — свойство изделия»). Всё, чего не хватало, — площадь; она существовала только в
// памяти браузера, потому что DXF разбирает клиент и только клиент.
//
// ЧТО ЗДЕСЬ ХРАНИТСЯ, И ЧЕГО ЗДЕСЬ НЕТ:
//   - площадь ОДНОГО ЭКЗЕМПЛЯРА контура, БЕЗ pieces_per_garment (оно живёт на детали и меняется
//     отдельной правкой карточки — вмноженное сюда, оно молча врало бы после каждой такой правки);
//   - условия замера, потому что без них площадь не число, а мнение (слой шва надо раздуть
//     припуском, слой кроя — уже нет);
//   - отпечаток набора листов, который считает СЕРВЕР, — им и определяется устаревание.
//
// Нормы здесь НЕТ и быть не может: норма — это площадь, делённая на раскройную ширину КОНКРЕТНОГО
// артикула конкретного колорвея, а ширина живёт на артикуле. Смешать их значило бы заморозить
// ширину в геометрии.

// PieceAreaBlockRef is one блок→деталь link as the source fingerprint sees it.
type PieceAreaBlockRef struct {
	BlockName    string
	PieceLineKey string
}

// PieceAreaSourceFingerprint hashes EVERYTHING the measurement was derived from: the scope's sheet
// set AND its блок→деталь links.
//
// THE SHEETS ALONE ARE NOT THE SOURCE, and treating them as such was a real hole. A block can be
// re-pointed at another piece — or a piece deleted, cascading its link away — WITHOUT any sheet
// being re-uploaded. The url/version of every file then still matches, the areas read as current,
// and they describe a piece the card no longer cuts from that fabric. Folding the links in makes
// that event move the fingerprint by itself, which is the whole point of having one.
//
// Sorted and case-folded for the same reasons PatternSheetFingerprint does it: the answer must not
// depend on a query's ORDER BY, and it must survive the collation difference between prod (utf8mb3)
// and a test container (utf8mb4).
func PieceAreaSourceFingerprint(sheets []PatternSheetRef, blocks []PieceAreaBlockRef) string {
	type blockEntry struct {
		B string `json:"b"`
		P string `json:"p"`
	}
	projection := make([]blockEntry, 0, len(blocks))
	for _, b := range blocks {
		projection = append(projection, blockEntry{
			B: strings.ToUpper(strings.TrimSpace(b.BlockName)),
			P: strings.ToUpper(strings.TrimSpace(b.PieceLineKey)),
		})
	}
	sort.Slice(projection, func(i, j int) bool {
		if projection[i].B != projection[j].B {
			return projection[i].B < projection[j].B
		}
		return projection[i].P < projection[j].P
	})
	blob, err := json.Marshal(projection)
	if err != nil {
		// The projection is two strings per entry; Marshal cannot fail on it. Hashing the error
		// text instead of panicking keeps a store write from taking the process down, and the value
		// still differs from every real one, so it reads as «changed» rather than as «current».
		blob = []byte("piece-area-blocks-encode-error:" + err.Error())
	}
	sum := sha256.Sum256(append([]byte(PatternSheetFingerprint(sheets)+"|"), blob...))
	return hex.EncodeToString(sum[:])
}

// PieceAreaRow is one stored contour area: which piece, in which fabric scope, at which size.
//
// SizeId is NULL for a piece that does not grade — it enters EVERY size's set whole, the same rule
// MarkerSizeAreasPerGarment already applies to a marker's ungraded pieces. A reader that turns NULL
// into «size 0» would give that piece to one size and steal it from the rest.
type PieceAreaRow struct {
	Id           int    `db:"id"`
	TechCardId   int    `db:"tech_card_id"`
	ScopeKey     string `db:"scope_key"`
	PieceLineKey string `db:"piece_line_key"`
	// SizeId NULL = ungraded piece (part of every size's set).
	SizeId sql.NullInt64 `db:"size_id"`
	// AreaCm2 is ONE instance's area in cm², under the conditions below.
	AreaCm2 decimal.Decimal `db:"area_cm2"`

	ContourLayer    string          `db:"contour_layer"`
	SeamAllowanceMm decimal.Decimal `db:"seam_allowance_mm"`
	// Hulled: the contour was replaced by its convex hull while being inflated by the seam
	// allowance — the area is larger than the true one but deterministic.
	Hulled bool `db:"hulled"`
	// AmbiguousPick: the layer carried several candidates of equal area and the first was taken —
	// the number depends on sheet order in the pack, so nothing may be *compared* against it.
	// Deliberately NOT merged with Hulled: one is a known overstatement, the other a known
	// irreproducibility, and they lead to different sentences on screen.
	AmbiguousPick bool `db:"ambiguous_pick"`

	SheetFingerprint string    `db:"sheet_fingerprint"`
	ParsedBy         string    `db:"parsed_by"`
	ParsedAt         time.Time `db:"parsed_at"`
}

// PieceAreaWrite is one scope's WHOLE parsed set, as the client submits it.
//
// SheetLineKeys is the list of sheets the client actually parsed. The server compares it against the
// scope's current membership and refuses a mismatch in BOTH directions — the same refusal
// PutTechCardPatternSizeIndex makes, for the same reason: an area set computed over a different set
// of files answers confidently for files nobody read, and a partial parse is not a subset of the
// full answer (a missing piece lowers the garment's area, a lower area lowers the norm, and that is
// discovered in the warehouse, not on screen).
type PieceAreaWrite struct {
	TechCardId    int
	ScopeKey      string
	SheetLineKeys []string
	Rows          []PieceAreaInput
	ParsedBy      string
}

// PieceAreaInput is one row of the submitted set.
type PieceAreaInput struct {
	PieceLineKey    string
	SizeId          sql.NullInt64
	AreaCm2         decimal.Decimal
	ContourLayer    string
	SeamAllowanceMm decimal.Decimal
	Hulled          bool
	AmbiguousPick   bool
}

// PieceAreaResult is what the server recorded, echoed back so the client can show provenance
// without a second read.
type PieceAreaResult struct {
	ScopeKey         string
	SheetFingerprint string
	Stored           int
}

// PieceAreaScope groups a card's stored areas by fabric scope, with the staleness verdict already
// resolved. Staleness is COMPUTED, not stored: a stored flag would need somebody to maintain it and
// would be wrong exactly when a sheet is replaced without anyone opening the card.
type PieceAreaScope struct {
	ScopeKey string
	Rows     []PieceAreaRow
	// Stale is true when the scope's CURRENT sheet fingerprint differs from the one the areas were
	// measured under — the patterns changed since the measurement.
	Stale bool
	// CurrentFingerprint is the scope's fingerprint right now, for the message that has to say
	// «measured on {date}, patterns changed since».
	CurrentFingerprint string
}
