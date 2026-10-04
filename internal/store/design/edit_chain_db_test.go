package design_test

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/rubenv/sql-migrate/sqlparse"
	"github.com/stretchr/testify/require"
)

// ═══ UNDO / REDO ЦЕПОЧКИ ПРАВОК — 0387, T28 v2 ═════════════════════════════════════════════════
//
// Undo и redo — одна SERIALIZABLE-транзакция каждый: замок карточки → звенья цепочки → слоты, CAS по
// expected_current_id + expected_target_id, метка undone_at и переезд слота через setBenchSlotTx.
// Решения проверены без базы (entity/design_edit_chain_test.go); здесь — что легло в строки.
//
// Запуск — тот же одноразовый контейнер, что и у соседних проб (см. шапку wave2_db_test.go); без
// CI=1 каждая проба пропускается ДО открытия соединения.

// chainProbe — A в слоте `front`, перезаписанный правкой B: слот на B, A.replaced_by = B.
type chainProbe struct {
	replaceProbeSetup
	edit *entity.DesignPicture
}

func newChainProbe(t *testing.T, rep dependency.Repository, raw *sql.DB) chainProbe {
	t.Helper()
	p := newReplaceProbeSetup(t, rep, raw)
	edit, err := rep.Design().FlattenEditLayer(context.Background(), p.overwrite(probeMedia(t, raw), p.sheet.Id))
	require.NoError(t, err)
	holder, _, _ := probeSlotHolder(t, raw, p.slot.Id)
	require.EqualValues(t, edit.Id, holder.Int32)
	return chainProbe{replaceProbeSetup: p, edit: edit}
}

func (c chainProbe) undo(rep dependency.Repository) (*entity.DesignEditChainResult, error) {
	return rep.Design().UndoEdit(context.Background(), entity.DesignEditChainStepRequest{
		PictureId: c.sheet.Id, ExpectedCurrentId: c.edit.Id, ExpectedTargetId: c.sheet.Id,
		IdempotencyKey: uuid.NewString(), Actor: "undoer",
	})
}

func (c chainProbe) redo(rep dependency.Repository) (*entity.DesignEditChainResult, error) {
	return rep.Design().RedoEdit(context.Background(), entity.DesignEditChainStepRequest{
		PictureId: c.sheet.Id, ExpectedCurrentId: c.sheet.Id, ExpectedTargetId: c.edit.Id,
		IdempotencyKey: uuid.NewString(), Actor: "redoer",
	})
}

// probeUndoneAt — метка отмены, мимо стора.
func probeUndoneAt(t *testing.T, raw *sql.DB, pictureID int) sql.NullTime {
	t.Helper()
	var got sql.NullTime
	require.NoError(t, raw.QueryRow(`SELECT undone_at FROM design_picture WHERE id = ?`, pictureID).Scan(&got))
	return got
}

func resultPicture(t *testing.T, res *entity.DesignEditChainResult, id int) entity.DesignPicture {
	t.Helper()
	for _, p := range res.Pictures {
		if p.Id == id {
			return p
		}
	}
	t.Fatalf("picture %d is not in the chain result", id)
	return entity.DesignPicture{}
}

// UNDO ВОЗВРАЩАЕТ СЛОТ НА A И МЕТИТ B; REDO ВОЗВРАЩАЕТ ОБА.
func TestDesignDBUndoRedoMovesTheSlotAndTheMark(t *testing.T) {
	rep, raw := probeRepository(t)
	c := newChainProbe(t, rep, raw)
	_, rev0, _ := probeSlotHolder(t, raw, c.slot.Id)

	res, err := c.undo(rep)
	require.NoError(t, err)
	require.Equal(t, c.sheet.Id, res.CurrentPictureId)
	holder, rev, setBy := probeSlotHolder(t, raw, c.slot.Id)
	require.EqualValues(t, c.sheet.Id, holder.Int32, "undo возвращает слот на A")
	require.Equal(t, rev0+1, rev, "переезд — постановка, ревизия растёт")
	require.Equal(t, "undoer", setBy)
	require.True(t, probeUndoneAt(t, raw, c.edit.Id).Valid, "B отменён")
	require.False(t, probeUndoneAt(t, raw, c.sheet.Id).Valid)
	require.EqualValues(t, c.edit.Id, probeReplacedBy(t, raw, c.sheet.Id).Int32, "цепочка не рвётся")
	var hidden bool
	require.NoError(t, raw.QueryRow(`SELECT hidden_at IS NOT NULL FROM design_picture WHERE id = ?`, c.edit.Id).Scan(&hidden))
	require.False(t, hidden, "undo не трогает hidden_at")
	require.Len(t, res.Slots, 1)
	require.Equal(t, c.slot.Id, res.Slots[0].Id)
	a := resultPicture(t, res, c.sheet.Id)
	require.False(t, a.CanUndo)
	require.True(t, a.CanRedo, "A текущий, за ним отменённый B")

	res, err = c.redo(rep)
	require.NoError(t, err)
	require.Equal(t, c.edit.Id, res.CurrentPictureId)
	holder, rev, setBy = probeSlotHolder(t, raw, c.slot.Id)
	require.EqualValues(t, c.edit.Id, holder.Int32, "redo возвращает слот на B")
	require.Equal(t, rev0+2, rev)
	require.Equal(t, "redoer", setBy)
	require.False(t, probeUndoneAt(t, raw, c.edit.Id).Valid, "redo снимает метку")
	b := resultPicture(t, res, c.edit.Id)
	require.True(t, b.CanUndo)
	require.False(t, b.CanRedo)
	require.Equal(t, c.sheet.Id, b.UndoToId)

	// И полоса видит то же (resolveMedia → углы).
	band, err := rep.Design().GetBand(context.Background(), c.card, 12)
	require.NoError(t, err)
	var front *entity.DesignBenchSlot
	for i := range band.Bench {
		if band.Bench[i].Id == c.slot.Id {
			front = &band.Bench[i]
		}
	}
	require.NotNil(t, front)
	require.NotNil(t, front.Picture)
	require.Equal(t, c.edit.Id, front.Picture.Id)
	require.True(t, front.Picture.CanUndo)
}

// CAS: НЕВЕРНЫЕ expected — stale_chain, И НЕ ПИШЕТСЯ НИЧЕГО.
func TestDesignDBUndoRedoStaleChainWritesNothing(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	c := newChainProbe(t, rep, raw)
	_, rev0, _ := probeSlotHolder(t, raw, c.slot.Id)

	cases := []struct {
		name string
		undo bool
		req  entity.DesignEditChainStepRequest
	}{
		{"undo: текущей названа A", true, entity.DesignEditChainStepRequest{
			PictureId: c.sheet.Id, ExpectedCurrentId: c.sheet.Id, ExpectedTargetId: c.sheet.Id}},
		{"undo: цель не предшественник", true, entity.DesignEditChainStepRequest{
			PictureId: c.sheet.Id, ExpectedCurrentId: c.edit.Id, ExpectedTargetId: c.edit.Id}},
		// (redo с expected=A, target=B при текущей B — не stale, а ПОВТОР redo: см. ReplayWritesNothing.)
		{"redo: за текущей B нет отменённого звена", false, entity.DesignEditChainStepRequest{
			PictureId: c.sheet.Id, ExpectedCurrentId: c.edit.Id, ExpectedTargetId: c.sheet.Id}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.req.IdempotencyKey, tc.req.Actor = uuid.NewString(), "stale"
			var err error
			if tc.undo {
				_, err = rep.Design().UndoEdit(ctx, tc.req)
			} else {
				_, err = rep.Design().RedoEdit(ctx, tc.req)
			}
			if tc.undo {
				require.ErrorIs(t, err, entity.ErrDesignStaleChain)
			} else {
				require.ErrorIs(t, err, entity.ErrDesignNothingToRedo)
			}
			holder, rev, _ := probeSlotHolder(t, raw, c.slot.Id)
			require.EqualValues(t, c.edit.Id, holder.Int32)
			require.Equal(t, rev0, rev, "отказ не двигает слот")
			require.False(t, probeUndoneAt(t, raw, c.edit.Id).Valid)
		})
	}

	// После undo устаревший redo с ожиданием «текущая — B» — тоже stale.
	_, err := c.undo(rep)
	require.NoError(t, err)
	_, err = rep.Design().RedoEdit(ctx, entity.DesignEditChainStepRequest{
		PictureId: c.sheet.Id, ExpectedCurrentId: c.edit.Id, ExpectedTargetId: c.edit.Id,
		IdempotencyKey: uuid.NewString(), Actor: "stale"})
	require.ErrorIs(t, err, entity.ErrDesignStaleChain)
	require.True(t, probeUndoneAt(t, raw, c.edit.Id).Valid)
}

// ПОВТОР ТОГО ЖЕ ЖЕСТА — OK БЕЗ ЗАПИСИ.
func TestDesignDBUndoRedoReplayWritesNothing(t *testing.T) {
	rep, raw := probeRepository(t)
	c := newChainProbe(t, rep, raw)

	_, err := c.undo(rep)
	require.NoError(t, err)
	_, rev1, _ := probeSlotHolder(t, raw, c.slot.Id)
	mark := probeUndoneAt(t, raw, c.edit.Id)
	require.True(t, mark.Valid)
	time.Sleep(5 * time.Millisecond)

	res, err := c.undo(rep)
	require.NoError(t, err, "повтор undo — успех")
	require.Equal(t, c.sheet.Id, res.CurrentPictureId)
	_, rev, _ := probeSlotHolder(t, raw, c.slot.Id)
	require.Equal(t, rev1, rev, "повтор не двигает слот")
	require.True(t, mark.Time.Equal(probeUndoneAt(t, raw, c.edit.Id).Time), "повтор не переписывает метку")
	require.Len(t, res.Slots, 1, "ответ повтора несёт слот текущей версии")

	_, err = c.redo(rep)
	require.NoError(t, err)
	_, rev2, _ := probeSlotHolder(t, raw, c.slot.Id)
	require.Equal(t, rev1+1, rev2)
	res, err = c.redo(rep)
	require.NoError(t, err, "повтор redo — успех")
	require.Equal(t, c.edit.Id, res.CurrentPictureId)
	_, rev, _ = probeSlotHolder(t, raw, c.slot.Id)
	require.Equal(t, rev2, rev)
	require.False(t, probeUndoneAt(t, raw, c.edit.Id).Valid)
}

// ПЕРЕЗАПИСЬ ПОВЕРХ ОТМЕНЁННОГО ПРЕЕМНИКА ПРОХОДИТ; ПОВЕРХ ПРОСТО СПРЯТАННОГО — already_replaced.
func TestDesignDBOverwriteOverAnUndoneSuccessor(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()

	t.Run("отменённый преемник уступает место", func(t *testing.T) {
		c := newChainProbe(t, rep, raw)
		_, err := c.undo(rep)
		require.NoError(t, err)
		_, rev0, _ := probeSlotHolder(t, raw, c.slot.Id)

		// Тот же слой над файлом A (один слой на подложку) — новая правка «на место» A.
		c2, err := rep.Design().FlattenEditLayer(ctx, c.overwrite(probeMedia(t, raw), c.sheet.Id))
		require.NoError(t, err, "отменённый B место не держит")
		require.EqualValues(t, c2.Id, probeReplacedBy(t, raw, c.sheet.Id).Int32, "A теперь заменён C")
		holder, rev, _ := probeSlotHolder(t, raw, c.slot.Id)
		require.EqualValues(t, c2.Id, holder.Int32, "слот на C")
		require.Equal(t, rev0+1, rev)
		require.True(t, probeUndoneAt(t, raw, c.edit.Id).Valid, "отрезанная ветка остаётся строками, отменённой")
		require.Equal(t, 1, countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE id = ?`, c.edit.Id))

		// Старый redo на B больше не проходит — цепочка A→C.
		_, err = c.redo(rep)
		require.ErrorIs(t, err, entity.ErrDesignStaleChain)
		// А undo C → A — проходит.
		_, err = rep.Design().UndoEdit(ctx, entity.DesignEditChainStepRequest{
			PictureId: c2.Id, ExpectedCurrentId: c2.Id, ExpectedTargetId: c.sheet.Id,
			IdempotencyKey: uuid.NewString(), Actor: "undoer"})
		require.NoError(t, err)
		holder, _, _ = probeSlotHolder(t, raw, c.slot.Id)
		require.EqualValues(t, c.sheet.Id, holder.Int32)
	})

	t.Run("спрятанный, но не отменённый преемник держит место", func(t *testing.T) {
		card := probeCard(t, raw)
		a := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)
		layer, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
			TechCardId: card, BaseMediaId: a.MediaId, Strokes: probeStrokes(), Actor: "probe",
		})
		require.NoError(t, err)
		overwriteA := func() (*entity.DesignPicture, error) {
			return rep.Design().FlattenEditLayer(ctx, entity.DesignEditLayerFlatten{
				TechCardId: card, LayerId: layer.Id, ExpectedRev: layer.Rev, MediaId: probeMedia(t, raw),
				ReplacePictureId: a.Id, Actor: "overwriter",
			})
		}
		b, err := overwriteA() // A не в слоте — B можно спрятать
		require.NoError(t, err)
		_, err = rep.Design().HidePicture(ctx, b.Id, true, "colleague")
		require.NoError(t, err)
		before := countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, card)

		_, err = overwriteA()
		requireHead(t, err, a.Id, b.Id)
		require.Equal(t, before, countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, card))
		require.EqualValues(t, b.Id, probeReplacedBy(t, raw, a.Id).Int32)
	})
}

// ВОССТАНОВЛЕННЫЙ ОРИГИНАЛ РЕЖЕТСЯ (M1), И REDO ПОСЛЕ ЭТОГО ОТКАЗЫВАЕТ live_crop_parent.
func TestDesignDBSplitOfARestoredOriginal(t *testing.T) {
	rep, raw := probeRepository(t)
	c := newChainProbe(t, rep, raw)
	_, err := c.undo(rep)
	require.NoError(t, err)

	crops := splitProbe(t, rep, raw, c.sheet.Id, entity.DesignViewBack)
	require.Len(t, crops, 1, "восстановленный оригинал — текущая версия, его режут")
	require.EqualValues(t, c.sheet.Id, crops[0].DerivedFrom.Int32)

	// Отменённый B не режется.
	_, err = rep.Design().SplitPicture(context.Background(), entity.DesignSplitRequest{
		PictureId: c.edit.Id, ClientRequestId: uuid.NewString(), Actor: "probe",
		Frames: []entity.DesignSplitFrame{{MediaId: probeMedia(t, raw), ViewKey: entity.DesignViewFront}},
	})
	require.ErrorIs(t, err, entity.ErrDesignUndonePicture)

	_, rev0, _ := probeSlotHolder(t, raw, c.slot.Id)
	_, err = c.redo(rep)
	require.ErrorIs(t, err, entity.ErrDesignLiveCropParent, "redo увёл бы место у разрезанного листа")
	_, rev, _ := probeSlotHolder(t, raw, c.slot.Id)
	require.Equal(t, rev0, rev)
	require.True(t, probeUndoneAt(t, raw, c.edit.Id).Valid)
}

// ОТКАЗ ПЕРЕЕЗДА СЛОТА ОТКАТЫВАЕТ И МЕТКУ: undo — одна транзакция.
//
// Заменённый A можно поставить в другой слот (постановка заменённость не судит), и тогда undo B не
// может вернуть A в `front` — uq_design_bench_picture: picture_already_in_slot. Сегодня это отказ (а
// не «front пустеет» и не «A переезжает»); проба держит, что отказ не оставляет полуперехода — B не
// отменён, слоты не тронуты.
func TestDesignDBUndoRefusedBySlotMoveWritesNothing(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	c := newChainProbe(t, rep, raw)
	back, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: c.card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewBack},
		PictureId: c.sheet.Id, ExpectedSlotRev: 0, Actor: "probe",
	})
	require.NoError(t, err, "заменённый A встаёт в другой слот")
	_, rev0, _ := probeSlotHolder(t, raw, c.slot.Id)

	_, err = c.undo(rep)
	require.ErrorIs(t, err, entity.ErrDesignPictureAlreadyInSlot)
	require.False(t, probeUndoneAt(t, raw, c.edit.Id).Valid, "метка откатилась вместе с переездом")
	holder, rev, _ := probeSlotHolder(t, raw, c.slot.Id)
	require.EqualValues(t, c.edit.Id, holder.Int32)
	require.Equal(t, rev0, rev)
	holder, _, _ = probeSlotHolder(t, raw, back.Id)
	require.EqualValues(t, c.sheet.Id, holder.Int32)
}

// МИГРАЦИЯ 0387: применена автомиграцией, колонка DATETIME(6) NULL; Up идемпотентен, Down снимает
// колонку, Up возвращает. Файл разбирается тем же парсером, что у мигратора (sqlparse): в COMMENT
// колонки стоит «;», и наивное разбиение по «;» (runMigrationUp) рвёт оператор посередине.
func TestDesignDBMigration0387(t *testing.T) {
	_, raw := probeRepository(t)
	ctx := context.Background()
	column := func() (string, bool) {
		var typ, nullable string
		err := raw.QueryRow(`SELECT COLUMN_TYPE, IS_NULLABLE FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture' AND COLUMN_NAME = 'undone_at'`).
			Scan(&typ, &nullable)
		if errors.Is(err, sql.ErrNoRows) {
			return "", false
		}
		require.NoError(t, err)
		return typ + " " + nullable, true
	}
	got, ok := column()
	require.True(t, ok)
	require.Equal(t, "datetime(6) YES", got)
	require.Equal(t, 1, countRows(t, raw,
		`SELECT COUNT(*) FROM gorp_migrations WHERE id = '0387_design_picture_undone_at.sql'`))

	f, err := os.Open("../sql/0387_design_picture_undone_at.sql")
	require.NoError(t, err)
	defer f.Close()
	parsed, err := sqlparse.ParseMigration(f)
	require.NoError(t, err)
	run := func(stmts []string) {
		conn, err := raw.Conn(ctx) // @-переменные живут в сессии
		require.NoError(t, err)
		defer conn.Close()
		for _, s := range stmts {
			_, err := conn.ExecContext(ctx, s)
			require.NoError(t, err, s)
		}
	}
	t.Cleanup(func() { run(parsed.UpStatements) }) // колонка нужна соседям при любом исходе

	run(parsed.UpStatements)
	got, ok = column()
	require.True(t, ok, "повторный Up — no-op")
	require.Equal(t, "datetime(6) YES", got)
	run(parsed.DownStatements)
	_, ok = column()
	require.False(t, ok, "Down снимает колонку")
	run(parsed.UpStatements)
	got, ok = column()
	require.True(t, ok, "Up возвращает колонку")
	require.Equal(t, "datetime(6) YES", got)
}

// deadlockCounter — slog-обёртка, считающая повторы транзакции после дедлока (store.tx пишет
// «retrying transaction after transient error» с mysql_code).
type deadlockCounter struct {
	slog.Handler
	mu sync.Mutex
	n  int
}

func (h *deadlockCounter) Handle(ctx context.Context, r slog.Record) error {
	if strings.Contains(r.Message, "retrying transaction") {
		h.mu.Lock()
		h.n++
		h.mu.Unlock()
	}
	return h.Handler.Handle(ctx, r)
}

func (h *deadlockCounter) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.n
}

// ДВЕ ВКЛАДКИ ЖМУТ UNDO/REDO ОДНОЙ ЦЕПОЧКИ: каждый исход — успех, повтор или stale_chain; ни одного
// дедлока наружу; в конце слот стоит на текущей версии, а число переездов сходится с меткой.
//
// И НИ ОДНОГО ДЕДЛОКА ВНУТРИ (C2): замок карточки идёт первым, звенья и слоты берутся сразу X, и шаги
// одной цепочки встают в очередь, а не ловят 1213 и повтор.
//
// МУТАЦИЯ: снять lockDesignCard / lockDesignBench и FOR UPDATE со звеньев — исходы те же (повторы
// обёртки их вытягивают), но 1213 появляется, и проба краснеет на счётчике повторов.
func TestDesignDBUndoRedoConcurrent(t *testing.T) {
	rep, raw := probeRepository(t)
	c := newChainProbe(t, rep, raw)
	_, rev0, _ := probeSlotHolder(t, raw, c.slot.Id)

	prev := slog.Default()
	// Свой обработчик, а не prev.Handler(): обёртка над обработчиком по умолчанию после SetDefault
	// замыкается сама на себя через пакет log и вешает прогон.
	retries := &deadlockCounter{Handler: slog.NewTextHandler(os.Stderr, nil)}
	slog.SetDefault(slog.New(retries))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const workers, iterations = 4, 15
	errs := make(chan error, workers*iterations)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				var err error
				// Половина вкладок решает по свежему чтению, половина жмёт вслепую.
				undo := (w+i)%2 == 0
				if w < workers/2 {
					var undone bool
					if qerr := raw.QueryRow(`SELECT undone_at IS NOT NULL FROM design_picture WHERE id = ?`, c.edit.Id).Scan(&undone); qerr != nil {
						errs <- qerr
						return
					}
					undo = !undone
				}
				if undo {
					_, err = c.undo(rep)
				} else {
					_, err = c.redo(rep)
				}
				if err != nil && !errors.Is(err, entity.ErrDesignStaleChain) {
					errs <- err
				}
			}
		}(w)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("undo/redo workers did not finish")
	}
	close(errs)
	for err := range errs {
		t.Errorf("unexpected error: %v", err)
	}

	holder, rev, _ := probeSlotHolder(t, raw, c.slot.Id)
	undone := probeUndoneAt(t, raw, c.edit.Id).Valid
	require.False(t, probeUndoneAt(t, raw, c.sheet.Id).Valid, "корень не отменяется никогда")
	if undone {
		require.EqualValues(t, c.sheet.Id, holder.Int32, "B отменён — слот на A")
		require.Equal(t, 1, (rev-rev0)%2, "нечётное число переездов")
	} else {
		require.EqualValues(t, c.edit.Id, holder.Int32, "B текущий — слот на B")
		require.Equal(t, 0, (rev-rev0)%2, "чётное число переездов")
	}
	require.Equal(t, 1, countRows(t, raw, `SELECT COUNT(*) FROM design_bench_slot WHERE tech_card_id = ?`, c.card))
	require.Zero(t, retries.count(), "шаги одной цепочки не дедлочат друг друга (порядок замков C2)")
	t.Logf("slot moved %d times; B undone=%v", rev-rev0, undone)
}
