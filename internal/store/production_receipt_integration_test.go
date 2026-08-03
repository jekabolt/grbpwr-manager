package store

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/accounting"
	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/rubenv/sql-migrate/sqlparse"
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
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", runID)
		})

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
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", runID)
		})

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
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", runID)
		})
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
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", runID)
	})

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
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", runID)
	})

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
	pending, deadCount, err := acc.CountReceiptPostingBacklog(ctx, time.Time{}, time.Now().UTC().Add(time.Hour))
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

	// H1 regression: an operator reverses the posted entry (a normal accounting operation). The
	// receipt is 'posted', but its live entry is gone — the scan must re-see it so the worker can
	// re-post the next version. A posting_status='pending' filter here wedged ClosePeriod forever.
	_, err = testDB.ExecContext(ctx, `
		UPDATE acct_journal_entry SET reversed_by = id
		WHERE source_type='production_receive' AND source_key = ?`, entry.SourceKey)
	require.NoError(t, err)
	require.True(t, inScan(), "a posted receipt whose entry was reversed re-enters the scan for a re-post")
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM acct_journal_line WHERE entry_id IN (SELECT id FROM acct_journal_entry WHERE source_type='production_receive' AND source_key = ?)", entry.SourceKey)
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM acct_journal_entry WHERE source_type='production_receive' AND source_key = ?", entry.SourceKey)
	})
}

// TestProductionReceiptLegacyRemapMigration replays the Up statements of the REAL 0231 + 0235
// migration files over a legacy-shaped world (received runs with stamped counts, one with a live
// '<run_id>'-keyed journal entry, one with no entry) and pins the beta-recovery semantics: the
// entry is remapped onto the receipt key family, the receipt whose money is already in the ledger
// comes out 'posted' (NOT stuck-pending — the scan's NOT-EXISTS would never drain it and the
// backlog gauge would cry wolf every tick), and the entry-less receipt stays 'pending' for the
// worker. The statements come from the files, not a copy, so the test fails if the migration
// drifts from what it proves.
func TestProductionReceiptLegacyRemapMigration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber: sql.NullString{String: "PRUN-REMAP", Valid: true}, Name: "Remap Coat", Stage: entity.TechCardStageProto,
		ApprovalState: entity.TechCardApprovalDraft, MeasurementUnit: entity.TechCardUnitMm,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.TechCards().DeleteTechCard(context.Background(), tcID) })

	// Two legacy-received runs: counts stamped straight onto the plan grid, status flipped by SQL —
	// the pre-receipt world, where no receipt row exists.
	mkLegacyReceived := func(t *testing.T) int {
		runID, err := s.ProductionRuns().CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: tcID, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 10}},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run_receipt WHERE run_id = ?", runID)
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", runID)
		})
		_, err = testDB.ExecContext(ctx, "UPDATE production_run_line SET received_qty = 7, defect_qty = 1 WHERE run_id = ?", runID)
		require.NoError(t, err)
		_, err = testDB.ExecContext(ctx, "UPDATE production_run SET status = 'received', received_at = NOW() WHERE id = ?", runID)
		require.NoError(t, err)
		return runID
	}
	runPosted := mkLegacyReceived(t)
	runUnposted := mkLegacyReceived(t)

	legacyKey := strconv.Itoa(runPosted)
	_, err = testDB.ExecContext(ctx, `
		INSERT INTO acct_journal_entry (occurred_at, description, source_type, source_key, created_by)
		VALUES (CURDATE(), 'legacy remap probe', 'production_receive', ?, 'system')`, legacyKey)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(),
			"DELETE FROM acct_journal_entry WHERE source_type='production_receive' AND description='legacy remap probe'")
	})

	// Replay the real migrations' Up statements (both are idempotent by design — this is exactly
	// the mid-file-crash re-run path they must survive).
	for _, file := range []string{"sql/0231_production_run_receipt.sql", "sql/0235_production_receive_source_key_receipts.sql"} {
		f, err := os.Open(file)
		require.NoError(t, err)
		parsed, err := sqlparse.ParseMigration(f)
		require.NoError(t, f.Close())
		require.NoError(t, err)
		for i, stmt := range parsed.UpStatements {
			_, err := testDB.ExecContext(ctx, stmt)
			require.NoError(t, err, "%s statement %d", file, i)
		}
	}

	// The entry moved to the receipt family, suffix-free (it had no :vN).
	var receiptPosted, receiptUnposted struct {
		Id     int
		Status string
	}
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id, posting_status FROM production_run_receipt WHERE run_id = ?", runPosted).
		Scan(&receiptPosted.Id, &receiptPosted.Status))
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id, posting_status FROM production_run_receipt WHERE run_id = ?", runUnposted).
		Scan(&receiptUnposted.Id, &receiptUnposted.Status))

	var remappedKey string
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT source_key FROM acct_journal_entry WHERE source_type='production_receive' AND description='legacy remap probe'").
		Scan(&remappedKey))
	require.Equal(t, "receipt:"+strconv.Itoa(receiptPosted.Id), remappedKey, "legacy key rewritten onto the receipt family")

	require.Equal(t, entity.ReceiptPostingPosted, receiptPosted.Status,
		"a receipt whose money is already in the ledger must not sit pending forever")
	require.Equal(t, entity.ReceiptPostingPending, receiptUnposted.Status,
		"an entry-less receipt stays pending for the worker")

	// And the backfilled lines mirror the stamped counts.
	var good, defect int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT good_qty, defect_qty FROM production_run_receipt_line WHERE receipt_id = ?", receiptPosted.Id).
		Scan(&good, &defect))
	require.Equal(t, 7, good)
	require.Equal(t, 1, defect)
}

// TestProductionReceiptGoodUnitsBookStockAndCostPrice pins the command's PRINCIPAL branch — the one
// every real receive takes and the one the deleted store-level ReceiveProductionRun test used to
// cover: good units land on each line's own product_size, the movement is journaled as
// production_received, the frozen valuation divides the run's manual costs by the good total, and
// update_cost_price seeds the product's cost_price from exactly that figure.
func TestProductionReceiptGoodUnitsBookStockAndCostPrice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	var sizeA int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT id FROM size WHERE sku_ord != 0 ORDER BY id LIMIT 1`).Scan(&sizeA))
	var langID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM language").Scan(&langID))

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
	})
	require.NoError(t, err)
	prices := make([]entity.ColorwayPriceInsert, 0)
	for _, c := range currency.RequiredCurrencies() {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: c, Price: decimal.NewFromInt(200)})
	}
	if len(prices) == 0 {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: "EUR", Price: decimal.NewFromInt(200)})
	}
	prodID, err := s.Products().AddProduct(ctx, &entity.ColorwayNew{
		Product: &entity.ColorwayInsert{
			ProductBodyInsert: entity.ColorwayBodyInsert{
				Brand: "ACME", Color: "black", ColorCode: "BLK", CountryOfOrigin: "IT",
				TopCategoryId: 1, TargetGender: entity.Unisex, Season: entity.SeasonSS,
			},
			ThumbnailMediaID: mediaID,
			Translations:     []entity.ColorwayTranslationInsert{{LanguageId: langID, Name: "RCPT-GOOD", Description: "d"}},
			Prices:           prices,
		},
		SizeMeasurements: []entity.SizeWithMeasurementInsert{
			{ProductSize: entity.VariantInsert{SizeId: sizeA, Quantity: decimal.NewFromInt(2)}},
		},
		MediaIds: []int{mediaID}, Tags: []entity.ColorwayTagInsert{}, Prices: prices,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", prodID) })

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber: sql.NullString{String: "PRUN-GOOD", Valid: true}, Name: "Good Coat", Stage: entity.TechCardStageProto,
		ApprovalState: entity.TechCardApprovalDraft, MeasurementUnit: entity.TechCardUnitMm,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.TechCards().DeleteTechCard(context.Background(), tcID) })

	P := s.ProductionRuns()
	runID, err := P.CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{
			ProductId: sql.NullInt32{Int32: int32(prodID), Valid: true}, SizeId: sizeA, PlannedQty: 10,
		}},
		Costs: []entity.ProductionRunCost{{
			Kind: entity.ProductionRunCostCMT, Amount: decimal.NewFromInt(90), Currency: "EUR",
			AmountBase: decimal.NullDecimal{Decimal: decimal.NewFromInt(90), Valid: true},
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM production_run_receipt WHERE run_id = ?", runID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product_stock_change_history WHERE reference_id = ?", "production_run:"+strconv.Itoa(runID))
		_, _ = testDB.ExecContext(cctx, "DELETE FROM production_run WHERE id = ?", runID)
	})

	run, err := P.GetProductionRun(ctx, runID)
	require.NoError(t, err)
	lineKey := run.Lines[0].LineKey

	var qtyBefore decimal.Decimal
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT quantity FROM product_size WHERE product_id = ? AND size_id = ?", prodID, sizeA).Scan(&qtyBefore))

	key, err := entity.MintProductionRunLineKey()
	require.NoError(t, err)
	lines := []entity.ProductionRunReceiptLineInput{{LineKey: lineKey, GoodQty: 6, DefectQty: 3}}
	res, err := P.PostProductionRunReceipt(ctx, entity.PostProductionRunReceiptParams{
		RunID: runID, Lines: lines, IdempotencyKey: key,
		RequestHash:     dto.HashProductionRunReceiptPayload(runID, lines, "", true),
		UpdateCostPrice: true,
		Username:        "tester",
		BaseCurrency:    "EUR",
	})
	require.NoError(t, err)
	require.True(t, res.CostPriceUpdated, "cost_price seeded from the frozen actual")

	// Stock: +6 good on the line's own variant, journaled as production_received.
	var qtyAfter decimal.Decimal
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT quantity FROM product_size WHERE product_id = ? AND size_id = ?", prodID, sizeA).Scan(&qtyAfter))
	require.True(t, qtyAfter.Sub(qtyBefore).Equal(decimal.NewFromInt(6)),
		"good units booked into product_size: before %s after %s", qtyBefore, qtyAfter)
	var journaled int
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM product_stock_change_history
		WHERE product_id = ? AND source = 'production_received' AND reference_id = ?`,
		prodID, "production_run:"+strconv.Itoa(runID)).Scan(&journaled))
	require.Equal(t, 1, journaled, "stock increment journaled")

	// Valuation frozen on the receipt: 90 EUR manual costs over 6 good units = 15.00.
	got, err := P.GetProductionRun(ctx, runID)
	require.NoError(t, err)
	require.Len(t, got.Receipts, 1)
	rc := got.Receipts[0]
	require.True(t, rc.HasBase)
	require.True(t, rc.UnitCostBase.Valid)
	require.True(t, rc.UnitCostBase.Decimal.Equal(decimal.NewFromInt(15)),
		"unit_cost_base = 90/6, got %s", rc.UnitCostBase.Decimal)
	require.Equal(t, "EUR", rc.BaseCurrency.String)

	// cost_price carries the same figure with production_run provenance.
	var costPrice decimal.NullDecimal
	var srcRun sql.NullInt32
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT cost_price, cost_price_production_run_id FROM product WHERE id = ?", prodID).
		Scan(&costPrice, &srcRun))
	require.True(t, costPrice.Valid)
	require.True(t, costPrice.Decimal.Equal(decimal.NewFromInt(15)), "cost_price = frozen actual, got %s", costPrice.Decimal)
	require.True(t, srcRun.Valid)
	require.EqualValues(t, runID, srcRun.Int32)
}
