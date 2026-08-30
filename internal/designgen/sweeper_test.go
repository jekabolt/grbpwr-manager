package designgen

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeSweepStore считает вызовы и умеет отвечать ошибкой на заданном по счёту.
type fakeSweepStore struct {
	mu       sync.Mutex
	calls    int
	failOn   int
	released int
	gate     chan struct{} // если не nil, подметание виснет на нём — так проба ловит тик НА ЛЕТУ
}

func (f *fakeSweepStore) ReviveExpiredRuns(ctx context.Context) (int, error) {
	f.mu.Lock()
	f.calls++
	n, fail, gate := f.calls, f.failOn, f.gate
	f.mu.Unlock()
	if gate != nil {
		// ЖДЁМ ТОЛЬКО ВОРОТА И НАМЕРЕННО НЕ СЛУШАЕМ ctx.Done(). Первая редакция этой подделки
		// слушала отмену и выходила из подметания сама — и тем ОПРОВЕРГАЛА собственную пробу:
		// Stop отменял контекст, подделка немедленно возвращалась, ждать было нечего, и проба
		// «Stop дожидается» проходила бы над несуществующим сценарием.
		//
		// Опасность, ради которой орган ждёт, — ровно противоположная: запись, которая УЖЕ
		// коммитится и отмену не увидит. Если Stop вернётся раньше неё, App.Stop закроет пул у
		// неё под руками. Подделка обязана моделировать именно такую запись.
		<-gate
	}
	if fail == n {
		return 0, errors.New("база недоступна")
	}
	f.mu.Lock()
	f.released++
	f.mu.Unlock()
	return 1, nil
}

func (f *fakeSweepStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeSweepStore) releases() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.released
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("не дождались: %s", what)
}

// TestSweeperKeepsSweepingAfterAFailedSweep — ГЛАВНОЕ УТВЕРЖДЕНИЕ ОРГАНА.
//
// Подметальщик существует ради одного: снять резерв со строк, которые иначе держат деньги дня до
// полуночи и не закрываются никогда. Если первая же ошибка базы гасит цикл, орган умирает молча в
// точности тогда, когда он нужен — база икнула, воркера нет (флаг выключен), и убирать больше
// некому вовсе. Поэтому проба утверждает не «подмёл», а «подмёл СНОВА ПОСЛЕ ПРОВАЛА».
func TestSweeperKeepsSweepingAfterAFailedSweep(t *testing.T) {
	store := &fakeSweepStore{failOn: 1}
	s := newSweeper(store, time.Millisecond)
	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(func() { _ = s.Stop() })

	waitFor(t, "подметание после провала", func() bool { return store.releases() >= 2 })
	require.GreaterOrEqual(t, store.count(), 3,
		"цикл обязан пережить ошибку базы: иначе резерв висит, а признака этого нет нигде")
}

// TestSweeperStopWaitsForTheSweepInFlight — останов обязан ДОЖДАТЬСЯ тика на лету.
//
// App.Stop гасит работников ДО закрытия пула базы. Подметание, уже снявшее резерв, дописывает это
// в базу; если Stop вернётся раньше, запись уйдёт в закрытый пул, и резерв останется занятым при
// том, что строка уже закрыта — худший из исходов, потому что следов не останется ни там, ни там.
func TestSweeperStopWaitsForTheSweepInFlight(t *testing.T) {
	gate := make(chan struct{})
	store := &fakeSweepStore{gate: gate}
	s := newSweeper(store, time.Millisecond)
	require.NoError(t, s.Start(context.Background()))

	waitFor(t, "подметание вошло в стор", func() bool { return store.count() >= 1 })

	done := make(chan struct{})
	go func() { _ = s.Stop(); close(done) }()

	select {
	case <-done:
		t.Fatal("Stop вернулся, пока подметание ещё внутри стора: запись уйдёт в закрытый пул")
	case <-time.After(60 * time.Millisecond):
	}

	close(gate)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop не вернулся после того, как подметание завершилось")
	}
}

// TestSweeperRefusesASecondStart — два цикла над одной очередью means два читателя одного правила.
func TestSweeperRefusesASecondStart(t *testing.T) {
	s := newSweeper(&fakeSweepStore{}, time.Hour)
	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(func() { _ = s.Stop() })
	require.Error(t, s.Start(context.Background()))
}

// TestSweeperIntervalIsNotTheWorkerInterval — интервалы РАЗНЫЕ, и это решение, а не совпадение.
//
// Подметальщик ждёт истечения лизы, которая мерится минутами; опрос с частотой воркера (5 секунд)
// не находит ничего быстрее, он только стучится в базу. Если кто-то однажды «унифицирует» эти два
// числа, пусть сначала уронит эту пробу и прочитает довод.
func TestSweeperIntervalIsNotTheWorkerInterval(t *testing.T) {
	d := DefaultConfig()
	require.NotEqual(t, d.WorkerInterval, sweeperInterval,
		"подметальщик ждёт лизу, а не работу: частота воркера здесь только стучится в базу")
	require.GreaterOrEqual(t, sweeperInterval, 30*time.Second)
	require.LessOrEqual(t, sweeperInterval, 5*time.Minute,
		"повисший резерв занимает деньги своего дня: час опоздания — заметная доля дневного потолка")
}
