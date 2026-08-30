package designgen

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/health"
)

// ПОДМЕТАЛЬЩИК РЕЗЕРВОВ — ОРГАН, РАБОТАЮЩИЙ ИМЕННО ТОГДА, КОГДА ГЕНЕРАЦИЯ ВЫКЛЮЧЕНА.
//
// ЗАЧЕМ ОН ЕСТЬ. Флаг DESIGN_GENERATION_ENABLED выключают ДЕПЛОЕМ, и в app.go воркер при этом не
// просто не запускается — он не КОНСТРУИРУЕТСЯ. Значит вместе с очередью замолкает и то, что
// очередью не является: ReviveExpiredRuns, которая доводит до терминала брошенные строки и
// ОТПУСКАЕТ ИХ РЕЗЕРВ. Строки, заведённые до выключения, остаются в `pending` с занятыми деньгами,
// и снять их некому НИКОГДА — предикат захвата их не берёт, а другого пути к терминалу нет.
//
// ПОЧЕМУ ОДНОРАЗОВОГО ПОДМЕТАНИЯ НА СТАРТЕ НЕ ХВАТИЛО БЫ. Выключение флага — это редеплой, и
// соблазн велик: подмести один раз при запуске и не держать цикл. Но ReviveExpiredRuns действует
// только на ИСТЁКШИЕ лизы, а лиза прежнего воркера в момент запуска нового ещё жива — она мерится
// минутами. Одноразовое подметание нашло бы ноль строк и больше не повторилось бы; сироты
// пережили бы ровно ту проверку, которая заведена против них.
//
// ЧЕГО ОН НЕ ДЕЛАЕТ И ПОЧЕМУ ЭТО ВАЖНО. Он не зовёт ClaimRuns и вообще ничего не тратит: у него
// нет ни одного провайдера, и взять прогон в работу он не может физически, а не по уговору. Это
// намеренно — «выключено» обязано означать «не тратит», иначе флаг перестаёт быть флагом. Он
// возвращает `running` без хозяина обратно в `pending` (безвредно: при выключенной генерации их
// всё равно никто не заберёт) и закрывает отменённые-и-брошенные, снимая резерв.
//
// КОГДА ФЛАГ ВКЛЮЧЁН, ЭТОГО ОРГАНА НЕТ. Ту же ReviveExpiredRuns на каждом тике зовёт сам воркер;
// второй подметальщик рядом был бы вторым читателем одного правила — тем самым классом, из-за
// которого в этой волне уже расходились два числа.
type Sweeper struct {
	store    reserveSweeperStore
	interval time.Duration

	ctx     context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
	tracker health.Tracker
}

// reserveSweeperStore — НАМЕРЕННО УЖЕ, чем runStore воркера. Подметальщику доступен ровно один
// глагол, и это не экономия строк: интерфейс — единственное место, где видно, что орган не в
// состоянии потратить деньги. Расширение этого списка обязано быть заметным решением.
type reserveSweeperStore interface {
	ReviveExpiredRuns(ctx context.Context) (int, error)
}

const sweeperName = "design-reserve-sweeper"

// sweeperInterval — минута, и она НЕ равна WorkerInterval (5 секунд).
//
// Подметальщик ждёт не работы, а ИСТЕЧЕНИЯ ЛИЗЫ, которая мерится минутами: опрос чаще неё ничего
// не находит быстрее, он только стучится в базу. Верхняя граница выбрана тем, чем измеряется вред:
// повисший резерв занимает деньги ТОЛЬКО СВОЕГО ДНЯ (design_budget_day ключуется днём), поэтому
// минута опоздания не стоит ничего, а час — уже заметная доля дневного потолка.
const sweeperInterval = time.Minute

// NewSweeper строит подметальщика над настоящим репозиторием.
//
// Провайдеров он не принимает НЕ ПО ЗАБЫВЧИВОСТИ: конструктор без них — это и есть доказательство,
// что орган не может позвать модель.
func NewSweeper(repo dependency.Repository) (*Sweeper, error) {
	if repo == nil {
		return nil, fmt.Errorf("designgen: подметальщику резервов нечего подметать без репозитория")
	}
	return newSweeper(repo.Design(), sweeperInterval), nil
}

// newSweeper — шов, которым пользуются пробы: тот же орган над поддельным стором и с интервалом,
// который не заставляет пробу ждать минуту. Тот же приём, что и newWorker рядом; ЭТОТ ПАКЕТ
// НИКОГДА НЕ ОТКРЫВАЕТ БАЗУ В ТЕСТЕ — вне CI TestMain стора читает продакшен-DSN и дропает все
// таблицы.
func newSweeper(store reserveSweeperStore, interval time.Duration) *Sweeper {
	return &Sweeper{store: store, interval: interval}
}

func (s *Sweeper) Name() string { return sweeperName }

func (s *Sweeper) LastSuccess() time.Time { return s.tracker.LastSuccess() }

// Start запускает цикл. Повторный запуск отвергается — по тому же правилу, что и у воркера.
func (s *Sweeper) Start(ctx context.Context) error {
	if s.ctx != nil && s.stop != nil {
		return fmt.Errorf("designgen: подметальщик резервов уже запущен")
	}
	s.ctx, s.stop = context.WithCancel(ctx)
	s.wg.Go(func() { s.run(s.ctx) })
	slog.Default().InfoContext(ctx, "design reserve sweeper started (generation is off; nothing here spends)",
		slog.Duration("interval", s.interval))
	return nil
}

// Stop гасит цикл и ДОЖИДАЕТСЯ тика на лету: App.Stop останавливает работников до закрытия базы,
// и подметание, уже снявшее резерв, обязано дописать это в живой пул.
func (s *Sweeper) Stop() error {
	if s.stop == nil {
		return nil
	}
	s.stop()
	s.wg.Wait()
	s.ctx, s.stop = nil, nil
	return nil
}

func (s *Sweeper) run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepOnce(ctx)
		}
	}
}

func (s *Sweeper) sweepOnce(ctx context.Context) {
	n, err := s.store.ReviveExpiredRuns(ctx)
	if err != nil {
		// Ошибка подметания не гасит цикл: она почти всегда про базу, а следующая минута — новая
		// попытка. Молчать здесь нельзя, иначе резерв висит, а признака этого нет нигде.
		s.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "design reserve sweeper: the sweep failed",
			slog.String("err", err.Error()))
		return
	}
	s.tracker.MarkSuccess()
	if n > 0 {
		slog.Default().InfoContext(ctx, "design reserve sweeper: stranded runs released their reservation",
			slog.Int("runs", n))
	}
}
