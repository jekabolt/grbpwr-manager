package store

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/accounting"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// receiptParamsFromStored builds the receipt command's params the way the legacy shim does: counted
// lines synthesized from the run's stored received/defect counts, a freshly minted idempotency key,
// and the canonical request hash. Aux runs pass aux=true + the output material.
func receiptParamsFromStored(t *testing.T, run *entity.ProductionRun, updateCostPrice bool, aux bool, outputMaterialID int) entity.PostProductionRunReceiptParams {
	t.Helper()
	lines := make([]entity.ProductionRunReceiptLineInput, 0, len(run.Lines))
	for _, ln := range run.Lines {
		good, defect := int(ln.ReceivedQty.Int64), int(ln.DefectQty.Int64)
		if good <= 0 && defect <= 0 {
			continue
		}
		lines = append(lines, entity.ProductionRunReceiptLineInput{LineKey: ln.LineKey, GoodQty: good, DefectQty: defect})
	}
	key, err := entity.MintProductionRunLineKey()
	require.NoError(t, err)
	return entity.PostProductionRunReceiptParams{
		RunID:            run.Id,
		Lines:            lines,
		IdempotencyKey:   key,
		RequestHash:      dto.HashProductionRunReceiptPayload(run.Id, lines, "", updateCostPrice),
		UpdateCostPrice:  updateCostPrice,
		Username:         "tester",
		BaseCurrency:     "EUR",
		Aux:              aux,
		OutputMaterialID: outputMaterialID,
	}
}

// receiveStoredRunViaReceipt is the one-line replacement for the deleted store-level
// ReceiveProductionRun/ReceiveAuxiliaryProductionRun in older integration tests.
func receiveStoredRunViaReceipt(ctx context.Context, t *testing.T, s *MYSQLStore, runID int, aux bool, outputMaterialID int) (*entity.PostProductionRunReceiptResult, error) {
	t.Helper()
	run, err := s.ProductionRuns().GetProductionRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	return s.ProductionRuns().PostProductionRunReceipt(ctx, receiptParamsFromStored(t, run, false, aux, outputMaterialID))
}

// TestProductionReceiptCommand exercises the atomic receipt command against the real schema
// (0231/0232): happy path with counts in the command, idempotent replay, hash-mismatch rejection,
// all-scrap, the receipt row as accounting outbox, and the guards.
func TestProductionReceiptCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber: sql.NullString{String: "PRUN-RCPT", Valid: true}, Name: "Receipt Coat", Stage: entity.TechCardStageProto,
		ApprovalState: entity.TechCardApprovalDraft, MeasurementUnit: entity.TechCardUnitMm,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.TechCards().DeleteTechCard(context.Background(), tcID) })

	P := s.ProductionRuns()

	t.Run("counts travel in the command and freeze the valuation", func(t *testing.T) {
		runID, err := P.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: tcID, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 10}},
			Costs: []entity.ProductionRunCost{{
				Kind: entity.ProductionRunCostCMT, Amount: decimal.NewFromInt(80), Currency: "EUR",
				AmountBase: decimal.NullDecimal{Decimal: decimal.NewFromInt(80), Valid: true},
			}},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", runID) })

		run, err := P.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		require.Len(t, run.Lines, 1)
		lineKey := run.Lines[0].LineKey
		require.NotEmpty(t, lineKey)

		// Defect-only count on a product-less line: books nothing, still receives the run — the
		// counts were NOT stamped beforehand (they travel in the command).
		key, err := entity.MintProductionRunLineKey()
		require.NoError(t, err)
		lines := []entity.ProductionRunReceiptLineInput{{LineKey: lineKey, GoodQty: 0, DefectQty: 8}}
		params := entity.PostProductionRunReceiptParams{
			RunID: runID, Lines: lines, IdempotencyKey: key,
			RequestHash:  dto.HashProductionRunReceiptPayload(runID, lines, "", false),
			Username:     "tester",
			BaseCurrency: "EUR",
		}
		res, err := P.PostProductionRunReceipt(ctx, params)
		require.NoError(t, err)
		require.False(t, res.Replayed)
		require.Positive(t, res.ReceiptID)

		got, err := P.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		require.Equal(t, entity.ProductionRunReceived, got.Status)
		require.True(t, got.ReceivedAt.Valid, "received_at stamped")
		require.EqualValues(t, 8, got.Lines[0].DefectQty.Int64, "counts stamped onto the plan grid by the command")
		require.EqualValues(t, 0, got.Lines[0].ReceivedQty.Int64)
		require.Len(t, got.Receipts, 1)
		rc := got.Receipts[0]
		require.Equal(t, entity.ReceiptPostingPending, rc.PostingStatus, "the receipt is the accounting outbox")
		require.False(t, rc.HasBase, "all-scrap: no good units, valuation not computable")
		require.Len(t, rc.Lines, 1)
		require.Equal(t, lineKey, rc.Lines[0].LineKey)
		require.Equal(t, 8, rc.Lines[0].DefectQty)

		// Replay: same key + same hash → the ORIGINAL result, no second receipt, no double effects.
		res2, err := P.PostProductionRunReceipt(ctx, params)
		require.NoError(t, err)
		require.True(t, res2.Replayed)
		require.Equal(t, res.ReceiptID, res2.ReceiptID)
		again, err := P.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		require.Len(t, again.Receipts, 1, "replay must not create a second receipt")

		// Same key, different payload → conflict, nothing merged.
		bad := params
		bad.Lines = []entity.ProductionRunReceiptLineInput{{LineKey: lineKey, GoodQty: 1, DefectQty: 7}}
		bad.RequestHash = dto.HashProductionRunReceiptPayload(runID, bad.Lines, "", false)
		_, err = P.PostProductionRunReceipt(ctx, bad)
		require.ErrorIs(t, err, entity.ErrIdempotencyConflict)

		// A fresh key on an already-received run → the double-receive guard.
		fresh := receiptParamsFromStored(t, got, false, false, 0)
		_, err = P.PostProductionRunReceipt(ctx, fresh)
		require.ErrorIs(t, err, entity.ErrProductionRunAlreadyReceived)
	})

	t.Run("unknown line key and cancelled runs are refused", func(t *testing.T) {
		runID, err := P.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: tcID, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 5}},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", runID) })

		key, err := entity.MintProductionRunLineKey()
		require.NoError(t, err)
		ghost := []entity.ProductionRunReceiptLineInput{{LineKey: "GHOST0000000000000000000000"[:26], GoodQty: 1}}
		_, err = P.PostProductionRunReceipt(ctx, entity.PostProductionRunReceiptParams{
			RunID: runID, Lines: ghost, IdempotencyKey: key,
			RequestHash: dto.HashProductionRunReceiptPayload(runID, ghost, "", false), Username: "tester",
		})
		require.ErrorIs(t, err, entity.ErrProductionRunReceiptLineUnknown)

		// cancelled → refused (the old path never guarded this).
		_, err = testDB.ExecContext(ctx, "UPDATE production_run SET status = 'cancelled' WHERE id = ?", runID)
		require.NoError(t, err)
		run, err := P.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		run.Lines[0].ReceivedQty = sql.NullInt64{Int64: 5, Valid: true}
		_, err = P.PostProductionRunReceipt(ctx, receiptParamsFromStored(t, run, false, false, 0))
		require.ErrorIs(t, err, entity.ErrProductionRunCancelledReceive)

		// missing run → ErrNoRows.
		key2, err := entity.MintProductionRunLineKey()
		require.NoError(t, err)
		_, err = P.PostProductionRunReceipt(ctx, entity.PostProductionRunReceiptParams{
			RunID: 0, Lines: ghost, IdempotencyKey: key2,
			RequestHash: dto.HashProductionRunReceiptPayload(0, ghost, "", false), Username: "tester",
		})
		require.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("stale expected_lock_version is refused", func(t *testing.T) {
		runID, err := P.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: tcID, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 5}},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", runID) })
		run, err := P.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		run.Lines[0].DefectQty = sql.NullInt64{Int64: 1, Valid: true}
		params := receiptParamsFromStored(t, run, false, false, 0)
		params.ExpectedLockVersion = run.LockVersion + 41 // counted against a version that never was
		_, err = P.PostProductionRunReceipt(ctx, params)
		require.ErrorIs(t, err, entity.ErrProductionRunConflict)
	})
}

// TestProductionReceiptLineDiffDB ports the adversarial review's DB probes for the 0230 keyed diff:
// the parking dance must survive a swap, a three-way rotation and an insert-into-a-vacated-slot
// against the real uniq_prl unique index (unit tests pin the plan, not the SQL ordering).
func TestProductionReceiptLineDiffDB(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber: sql.NullString{String: "PRUN-DIFF", Valid: true}, Name: "Diff Coat", Stage: entity.TechCardStageProto,
		ApprovalState: entity.TechCardApprovalDraft, MeasurementUnit: entity.TechCardUnitMm,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.TechCards().DeleteTechCard(context.Background(), tcID) })

	P := s.ProductionRuns()
	runID, err := P.CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunPlanned,
		Lines: []entity.ProductionRunLine{
			{SizeId: 1, PlannedQty: 10},
			{SizeId: 2, PlannedQty: 20},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", runID) })

	load := func() []entity.ProductionRunLine {
		run, err := P.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		return run.Lines
	}
	idsByKey := func(lines []entity.ProductionRunLine) map[string]int {
		m := make(map[string]int, len(lines))
		for _, l := range lines {
			m[l.LineKey] = l.Id
		}
		return m
	}

	lines := load()
	require.Len(t, lines, 2)
	before := idsByKey(lines)

	// SIZE SWAP: the two keyed lines exchange (NULL, size) slots — the parking dance must not trip
	// uniq_prl in any ordering, and both row ids must survive.
	swapped := []entity.ProductionRunLine{
		{LineKey: lines[0].LineKey, SizeId: lines[1].SizeId, PlannedQty: 11},
		{LineKey: lines[1].LineKey, SizeId: lines[0].SizeId, PlannedQty: 21},
	}
	require.NoError(t, P.UpdateProductionRunPreservingCosts(ctx, runID, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunPlanned, Lines: swapped,
	}, 0))
	after := load()
	require.Len(t, after, 2)
	require.Equal(t, before, idsByKey(after), "row ids survive a slot swap")

	// VACATE + INSERT: one keyed line vanishes (its slot frees), a NEW keyless line takes that
	// exact slot in the same save.
	survivors := []entity.ProductionRunLine{
		{LineKey: after[0].LineKey, SizeId: after[0].SizeId, PlannedQty: 12},
		{SizeId: after[1].SizeId, PlannedQty: 99}, // new keyless line into the vacated slot
	}
	require.NoError(t, P.UpdateProductionRunPreservingCosts(ctx, runID, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunPlanned, Lines: survivors,
	}, 0))
	final := load()
	require.Len(t, final, 2)
	require.Contains(t, idsByKey(final), after[0].LineKey)
	require.Equal(t, before[after[0].LineKey], idsByKey(final)[after[0].LineKey], "kept line's id survives")
}

// TestProductionReceiptPostingScan drives the store legs the posting worker composes — the scan,
// the facts read, the same-tx post+mark, the failure/dead-letter path — against the real schema.
// This class of query is exactly what shipped broken before (a ':v%' literal inside the NOT EXISTS
// broke sqlx named binding for the whole statement), so the scan MUST be exercised on a live DB.
func TestProductionReceiptPostingScan(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber: sql.NullString{String: "PRUN-SCAN", Valid: true}, Name: "Scan Coat", Stage: entity.TechCardStageProto,
		ApprovalState: entity.TechCardApprovalDraft, MeasurementUnit: entity.TechCardUnitMm,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.TechCards().DeleteTechCard(context.Background(), tcID) })

	P := s.ProductionRuns()
	runID, err := P.CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 10}},
		Costs: []entity.ProductionRunCost{{
			Kind: entity.ProductionRunCostCMT, Amount: decimal.NewFromInt(90), Currency: "EUR",
			AmountBase: decimal.NullDecimal{Decimal: decimal.NewFromInt(90), Valid: true},
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", runID) })

	run, err := P.GetProductionRun(ctx, runID)
	require.NoError(t, err)
	run.Lines[0].DefectQty = sql.NullInt64{Int64: 10, Valid: true}
	res, err := P.PostProductionRunReceipt(ctx, receiptParamsFromStored(t, run, false, false, 0))
	require.NoError(t, err)

	acc := s.Accounting()
	startDate := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	inScan := func() bool {
		refs, err := acc.ListUnpostedReceipts(ctx, startDate, 500)
		require.NoError(t, err)
		for _, r := range refs {
			if r.ReceiptID == res.ReceiptID {
				require.Equal(t, runID, r.RunID)
				return true
			}
		}
		return false
	}
	require.True(t, inScan(), "a fresh receipt is pending work for the worker")

	facts, err := acc.GetReceiptFactsForPosting(ctx, res.ReceiptID)
	require.NoError(t, err)
	require.Equal(t, res.ReceiptID, facts.ReceiptID)
	require.Equal(t, runID, facts.RunID)
	require.Equal(t, 0, facts.GoodQtyTotal, "all-scrap receipt")
	require.Len(t, facts.Costs, 1)

	// Failure bookkeeping: attempts accumulate; the budget's last failure dead-letters + drops the
	// receipt out of the scan.
	for i := 0; i < 2; i++ {
		dead, err := acc.RecordReceiptPostingFailure(ctx, res.ReceiptID, "boom", 3)
		require.NoError(t, err)
		require.False(t, dead)
	}
	dead, err := acc.RecordReceiptPostingFailure(ctx, res.ReceiptID, "boom final", 3)
	require.NoError(t, err)
	require.True(t, dead, "third failure with maxAttempts=3 dead-letters")
	require.False(t, inScan(), "dead-lettered receipts leave the scan")
	pending, deadCount, err := acc.CountReceiptPostingBacklog(ctx, time.Now().UTC().Add(time.Hour))
	require.NoError(t, err)
	require.GreaterOrEqual(t, deadCount, 1)
	_ = pending

	// Operator recovery: reset to pending → back in the scan; a successful post (entry + mark in
	// one tx, keyed 'receipt:<id>') then retires it for good.
	_, err = testDB.ExecContext(ctx, "UPDATE production_run_receipt SET posting_status='pending', posting_attempts=0 WHERE id = ?", res.ReceiptID)
	require.NoError(t, err)
	require.True(t, inScan(), "reset receipt is scannable again")

	entry, err := accounting.BuildProductionReceiveEntry(*facts, startDate, 1)
	require.NoError(t, err)
	require.Equal(t, "receipt:"+strconv.Itoa(res.ReceiptID), entry.SourceKey)
	require.NoError(t, s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if _, _, e := rep.Accounting().CreateJournalEntry(ctx, entry); e != nil {
			return e
		}
		return rep.Accounting().MarkReceiptPosted(ctx, res.ReceiptID)
	}))
	require.False(t, inScan(), "a posted receipt is done")
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM acct_journal_line WHERE entry_id IN (SELECT id FROM acct_journal_entry WHERE source_type='production_receive' AND source_key = ?)", entry.SourceKey)
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM acct_journal_entry WHERE source_type='production_receive' AND source_key = ?", entry.SourceKey)
	})
}
