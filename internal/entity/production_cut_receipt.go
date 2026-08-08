package entity

import (
	"database/sql"
	"errors"
	"time"
)

// ПРИЁМКА КРОЯ (production_run_cut_receipt, migration 0287) — two numbers per pair (настил, размер):
// how much was ВЫКРОЕНО off the lay, and how much of it was ПРИНЯТО В ПОШИВ.
//
// THE CHAIN IS NAMED ONCE — in common/production.proto — and this file is the domain half of it:
//
//	заказано      production_run_line.planned_qty
//	выкроено      production_run_cut_receipt.cut_qty        ← new
//	принято в пошив  production_run_cut_receipt.accepted_qty ← new
//	сдано готовым  production_run_line.received_qty
//	брак          production_run_line.defect_qty
//
// EXISTING FIELDS ARE NOT RE-MEANT, and that is the whole point of a separate table. Re-reading
// received_qty as «принято в пошив» would open a double ledger in which «принято в пошив» and «сдано
// готовым» drift apart while both are called «принято» — and the receive flow, the stock booking and
// every margin figure downstream all read received_qty as finished units. The two new numbers go
// BETWEEN the old ones; nothing above or below them moves.
//
// ЗАКАЗАННОГО И СДАННОГО ЗДЕСЬ НЕТ, AND THEIR ABSENCE IS LOAD-BEARING. Showing them beside the two
// new numbers is the client's job, and it has to be, because the keys differ: a run line is keyed
// (колорвей, размер) while a настил is keyed (колорвей, слот BOM), so ONE colourway legitimately has
// SEVERAL настилы — different size blocks, different cloths. Hanging planned_qty off a receipt row
// would replicate one order quantity across every настил of that colourway, and any sum over receipt
// rows would then double the order. The client already holds the run's lines and joins the chain
// there, once.
//
// THE NUMBERS ARE NOT VALIDATED AGAINST EACH OTHER, and that too is a decision rather than an
// omission. «Заказано 10, выкроено 12, принято 10» is legitimate overcutting — the cutting room cuts
// with a margin — and «выкроено 12, принято 9» is legitimate cutting scrap. A discrepancy is SHOWN,
// never refused: a refusal would mean the system cannot be brought into correspondence with what
// physically happened, which is the one thing a report of a fact exists to do. The only checks are
// about the domain of each number on its own (non-negative), mirroring chk_prcr_cut_qty /
// chk_prcr_accepted_qty so a bad payload is refused with its field named instead of surfacing MySQL
// error 3819.

// ErrProductionRunCutReceiptNotFound is returned when the pair (настил, размер) addressed for
// deletion holds no receipt. Resolved with a SELECT, never with RowsAffected — see
// productionrun.DeleteCutReceipt.
var ErrProductionRunCutReceiptNotFound = errors.New("production run cut receipt not found")

// ErrProductionRunCutReceiptConflict is returned when two writers create the SAME pair at the same
// instant and uniq_prcr_lay_size refuses the second INSERT.
//
// There is no lock_version here, and its absence is deliberate. A настил is a PLAN being co-edited,
// so a second tab overwriting it silently loses somebody's decision — hence its optimistic lock. A
// receipt is a REPORT OF A NUMBER somebody read off the cutting table; the run's FOR UPDATE lock
// serialises the writers, the last one to speak is the one who counted last, and created_by /
// updated_by record who that was. What remains is the create/create race, and it is a conflict
// rather than an internal error: reload and retry lands on an update.
var ErrProductionRunCutReceiptConflict = errors.New("production run cut receipt was written concurrently; reload and retry")

// ProductionRunCutReceiptNoteMaxLen mirrors the note VARCHAR(512). Checked in Go so an over-long
// note is refused by name rather than truncated by the server's SQL mode — a silently shortened
// note is a report that lies about itself.
const ProductionRunCutReceiptNoteMaxLen = 512

// ProductionRunCutReceiptInsert is the writable half of ONE pair. It carries no key of its own:
// (lay_id, size_id) IS the identity (uniq_prcr_lay_size), because the pair is already named by the
// настил and by the size dictionary, and a client-minted ULID would be a key with no second reader.
//
// The write is an UPSERT BY PAIR, never a full replace of a lay's receipts: a pair the payload does
// not mention is not touched, and only DeleteCutReceipt removes one. That is the opposite of the
// настил's own sections (where the submitted list IS the lay) and it is deliberate — the cutting
// room reports the sizes that came off the table today, and treating its silence about the rest as
// «those sizes were never cut» would delete yesterday's count.
type ProductionRunCutReceiptInsert struct {
	SizeId      int
	CutQty      int
	AcceptedQty int
	Note        sql.NullString
}

// ProductionRunCutReceipt is one stored receipt row.
//
// LayKey and not LayId on the reading side: the настил is addressed by its STABLE key everywhere the
// API speaks of it (the lay_key world of 0281), and a reader that grouped rows by a database id
// would be holding an identity the client never sees. LayId is kept for the store's own joins.
type ProductionRunCutReceipt struct {
	Id     int    `db:"id"`
	LayId  int    `db:"lay_id"`
	LayKey string `db:"lay_key"`
	SizeId int    `db:"size_id"`
	// SizeName comes from the size dictionary so a reader can label the row without a second query;
	// the pair is what the row IS, and half of it arriving as a bare id would need resolving by
	// every caller.
	SizeName string `db:"size_name"`
	// CutQty — выкроено; AcceptedQty — принято в пошив. The two new numbers, and the only two this
	// table owns.
	CutQty      int            `db:"cut_qty"`
	AcceptedQty int            `db:"accepted_qty"`
	Note        sql.NullString `db:"note"`

	CreatedBy string    `db:"created_by"`
	UpdatedBy string    `db:"updated_by"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}
