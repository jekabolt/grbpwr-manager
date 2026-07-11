// Package fulfillment implements storage for the orders-fulfillment board: the
// board-owned annotation (assignee/notes/checklist) overlaid on orders, plus the
// board projection itself. Order STATUS is never stored here — the board reads it
// from the order and the ship/deliver transitions go through the order store, so
// the board cannot drift from an order's true status.
package fulfillment

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Bounds for the (historical) delivered column of the board.
const (
	defaultDeliveredLimit = 50
	maxDeliveredLimit     = 500
)

// TxFunc executes f within a transaction.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.Fulfillment.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

// New creates a new fulfillment store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

// boardRow is one order row on the board: the compact order plus its annotation
// summary (assignee/notes and checklist counts), assembled by a join.
type boardRow struct {
	entity.Order
	StatusName     entity.OrderStatusName `db:"status_name"`
	Assignee       string                 `db:"assignee"`
	Notes          string                 `db:"notes"`
	ChecklistTotal int                    `db:"checklist_total"`
	ChecklistDone  int                    `db:"checklist_done"`
}

// boardSelect is the shared projection + annotation join. The caller appends the
// status filter and ordering.
const boardSelect = `
	SELECT o.*, os.name AS status_name,
	       COALESCE(f.assignee, '') AS assignee,
	       COALESCE(f.notes, '') AS notes,
	       COALESCE(cl.total, 0) AS checklist_total,
	       COALESCE(cl.done, 0) AS checklist_done
	FROM customer_order o
	JOIN order_status os ON os.id = o.order_status_id
	LEFT JOIN order_fulfillment f ON f.order_uuid = o.uuid
	LEFT JOIN (
		SELECT order_fulfillment_id, COUNT(*) AS total,
		       CAST(COALESCE(SUM(is_done), 0) AS SIGNED) AS done
		FROM order_fulfillment_checklist_item
		GROUP BY order_fulfillment_id
	) cl ON cl.order_fulfillment_id = f.id`

// GetFulfillmentBoard returns the three columns as a projection of orders. The
// active columns (to_fulfill, shipped) list every matching order oldest-first
// (longest-waiting picked first); the historical delivered column is bounded and
// most-recent-first.
func (s *Store) GetFulfillmentBoard(ctx context.Context, deliveredLimit int) (*entity.FulfillmentBoard, error) {
	if deliveredLimit <= 0 {
		deliveredLimit = defaultDeliveredLimit
	}
	if deliveredLimit > maxDeliveredLimit {
		deliveredLimit = maxDeliveredLimit
	}

	board := &entity.FulfillmentBoard{}

	active, err := storeutil.QueryListNamed[boardRow](ctx, s.DB,
		boardSelect+`
		WHERE os.name IN (:toFulfill, :shipped)
		ORDER BY o.placed ASC, o.id ASC`,
		map[string]any{
			"toFulfill": string(entity.Confirmed),
			"shipped":   string(entity.Shipped),
		})
	if err != nil {
		return nil, fmt.Errorf("can't load active fulfillment orders: %w", err)
	}
	for i := range active {
		card := rowToCard(&active[i])
		switch card.Column {
		case entity.FulfillmentColumnToFulfill:
			board.ToFulfill = append(board.ToFulfill, card)
		case entity.FulfillmentColumnShipped:
			board.Shipped = append(board.Shipped, card)
		}
	}

	delivered, err := storeutil.QueryListNamed[boardRow](ctx, s.DB,
		boardSelect+`
		WHERE os.name = :delivered
		ORDER BY o.placed DESC, o.id DESC
		LIMIT :limit`,
		map[string]any{"delivered": string(entity.Delivered), "limit": deliveredLimit})
	if err != nil {
		return nil, fmt.Errorf("can't load delivered fulfillment orders: %w", err)
	}
	for i := range delivered {
		board.Delivered = append(board.Delivered, rowToCard(&delivered[i]))
	}

	return board, nil
}

func rowToCard(r *boardRow) entity.FulfillmentCard {
	return entity.FulfillmentCard{
		Order:          r.Order,
		Column:         entity.OrderStatusToFulfillmentColumn[r.StatusName],
		Assignee:       r.Assignee,
		ChecklistDone:  r.ChecklistDone,
		ChecklistTotal: r.ChecklistTotal,
		HasNotes:       strings.TrimSpace(r.Notes) != "",
	}
}

// GetOrderFulfillment returns an order's annotation with its checklist, or
// (nil, nil) when the order has no annotation yet.
func (s *Store) GetOrderFulfillment(ctx context.Context, orderUUID string) (*entity.OrderFulfillment, error) {
	f, err := storeutil.QueryNamedOne[entity.OrderFulfillment](ctx, s.DB,
		`SELECT id, order_uuid, assignee, notes, created_by, created_at, updated_at
		 FROM order_fulfillment WHERE order_uuid = :uuid`, map[string]any{"uuid": orderUUID})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("can't get order fulfillment: %w", err)
	}
	items, err := storeutil.QueryListNamed[entity.FulfillmentChecklistItem](ctx, s.DB,
		`SELECT id, order_fulfillment_id, content, is_done, position, created_at
		 FROM order_fulfillment_checklist_item WHERE order_fulfillment_id = :id
		 ORDER BY position, id`, map[string]any{"id": f.Id})
	if err != nil {
		return nil, fmt.Errorf("can't load fulfillment checklist: %w", err)
	}
	f.Checklist = items
	return &f, nil
}

// SetFulfillmentAssignee sets the order's fulfillment assignee, lazily creating
// the annotation.
func (s *Store) SetFulfillmentAssignee(ctx context.Context, orderUUID, assignee, createdBy string) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		id, err := ensureFulfillment(ctx, rep.DB(), orderUUID, createdBy)
		if err != nil {
			return err
		}
		return storeutil.ExecNamed(ctx, rep.DB(),
			`UPDATE order_fulfillment SET assignee = :assignee WHERE id = :id`,
			map[string]any{"assignee": assignee, "id": id})
	})
	if err != nil {
		return fmt.Errorf("can't set fulfillment assignee: %w", err)
	}
	return nil
}

// SetFulfillmentNotes sets the order's internal packing notes, lazily creating the
// annotation.
func (s *Store) SetFulfillmentNotes(ctx context.Context, orderUUID, notes, createdBy string) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		id, err := ensureFulfillment(ctx, rep.DB(), orderUUID, createdBy)
		if err != nil {
			return err
		}
		return storeutil.ExecNamed(ctx, rep.DB(),
			`UPDATE order_fulfillment SET notes = :notes WHERE id = :id`,
			map[string]any{"notes": nullString(notes), "id": id})
	})
	if err != nil {
		return fmt.Errorf("can't set fulfillment notes: %w", err)
	}
	return nil
}

// AddFulfillmentChecklistItem appends a packing-checklist item to an order,
// lazily creating the annotation. Returns the new item id.
func (s *Store) AddFulfillmentChecklistItem(ctx context.Context, orderUUID, content, createdBy string) (int, error) {
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		fid, err := ensureFulfillment(ctx, rep.DB(), orderUUID, createdBy)
		if err != nil {
			return err
		}
		pos, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COALESCE(MAX(position)+1, 0) FROM order_fulfillment_checklist_item WHERE order_fulfillment_id = :fid`,
			map[string]any{"fid": fid})
		if err != nil {
			return fmt.Errorf("failed to compute checklist position: %w", err)
		}
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(),
			`INSERT INTO order_fulfillment_checklist_item (order_fulfillment_id, content, position)
			 VALUES (:fid, :content, :position)`,
			map[string]any{"fid": fid, "content": content, "position": pos})
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("can't add fulfillment checklist item: %w", err)
	}
	return id, nil
}

// SetFulfillmentChecklistItemDone sets a checklist item's done flag. Returns
// sql.ErrNoRows when no item with the given id exists.
func (s *Store) SetFulfillmentChecklistItemDone(ctx context.Context, id int, done bool) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		exists, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM order_fulfillment_checklist_item WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("failed to check checklist item existence: %w", err)
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
		return storeutil.ExecNamed(ctx, rep.DB(),
			`UPDATE order_fulfillment_checklist_item SET is_done = :done WHERE id = :id`,
			map[string]any{"done": done, "id": id})
	})
	if err != nil {
		return fmt.Errorf("can't set fulfillment checklist item done: %w", err)
	}
	return nil
}

// DeleteFulfillmentChecklistItem removes a checklist item by id (idempotent).
func (s *Store) DeleteFulfillmentChecklistItem(ctx context.Context, id int) error {
	if err := storeutil.ExecNamed(ctx, s.DB,
		`DELETE FROM order_fulfillment_checklist_item WHERE id = :id`, map[string]any{"id": id}); err != nil {
		return fmt.Errorf("failed to delete fulfillment checklist item: %w", err)
	}
	return nil
}

// ensureFulfillment returns the id of the order's annotation row, creating it if
// absent. The ON DUPLICATE KEY UPDATE ... LAST_INSERT_ID(id) idiom makes
// LastInsertId return the existing row's id on conflict.
func ensureFulfillment(ctx context.Context, db dependency.DB, orderUUID, createdBy string) (int, error) {
	id, err := storeutil.ExecNamedLastId(ctx, db,
		`INSERT INTO order_fulfillment (order_uuid, created_by) VALUES (:uuid, :createdBy)
		 ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`,
		map[string]any{"uuid": orderUUID, "createdBy": createdBy})
	if err != nil {
		return 0, fmt.Errorf("failed to ensure order fulfillment: %w", err)
	}
	return id, nil
}

func nullString(s string) sql.NullString {
	s = strings.TrimSpace(s)
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
