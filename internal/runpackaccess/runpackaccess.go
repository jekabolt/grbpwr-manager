// Package runpackaccess — публичный НАРЯД НА ПАРТИЮ: минт токена-капабилити на прогон и отдача
// JSON-манифеста наряда по GET /api/rp/{token}.
//
// Точная копия посадки карточного вьюера выкроек (internal/patternaccess/viewer.go), и копия
// намеренная: свойства, ради которых там всё сделано именно так, здесь те же. Токен
// аутентифицирует САМ СЕБЯ — JWT в этом пути не участвует, потому что бумагу со швеиного стола
// открывают телефоном без логина. Любой отказ (битая подпись, отзыв, протухание, лимит) — один и
// тот же ГОЛЫЙ 404: перебирающий не должен уметь отличить «такого наряда нет» от «наряд отозван».
// Причина отказа уходит только в аудит, и уходит с сэмплированием.
//
// ЧЕГО В МАНИФЕСТЕ НЕТ И НЕ ПОЯВИТСЯ: денег. Ни план-костов, ни цен артикулов, ни актуалов, ни
// стоимости материала. Это не вопрос RBAC — на публичном эндпоинте нет аккаунта, под который можно
// было бы срезать костинг, поэтому безопасная форма не «прочитать и срезать», а «не читать»:
// entity.RunPack читает прогон явным списком колонок без денежных, а структуры ответа (manifest.go)
// объявлены здесь руками и денежных полей не имеют вовсе.
package runpackaccess

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/patterntoken"
	"github.com/jekabolt/grbpwr-manager/internal/ratelimit"
)

// Runs — узкий срез dependency.ProductionRuns, который нужен наряду. dependency.ProductionRuns его
// удовлетворяет; узкий он затем, чтобы тест этого пакета собирался на фейке в двадцать строк, а не
// на моке всего репозитория партий.
type Runs interface {
	EnsureRunPackAccess(ctx context.Context, runID int) (*entity.ProductionRunPackAccess, error)
	GetRunPackAccess(ctx context.Context, runID int) (*entity.ProductionRunPackAccess, error)
	RecordRunPackAccess(ctx context.Context, counts map[int]int64, last map[int]time.Time) error
	GetRunPack(ctx context.Context, runID int) (*entity.RunPack, error)
	GetRunPackMarkerSizes(ctx context.Context, markerIDs []int) (map[int][]entity.RunPackMarkerSize, error)
	ListLays(ctx context.Context, runID int) (entity.ProductionRunLayList, error)
}

// Cards — узкий срез dependency.TechCards: спецификация, по которой проецируется кат-лист.
// Целая карта здесь ВХОД, а не выход: dto.ComputeProductionRunCutPlan принимает *entity.TechCard, и
// узкого чтения под неё не существует. Деньги карты в ответ не попадают, потому что ответ собирают
// руками из полей, которых в нём нет, а не потому что кто-то их вычёркивает.
type Cards interface {
	GetTechCardById(ctx context.Context, id int) (*entity.TechCard, error)
}

const (
	// Пер (ip|прогон). Наряд открывают с телефона и перезагружают; бюджету достаточно покрывать
	// перезагрузки и повторные сканирования одной бумаги.
	perTokenWindow = time.Minute
	perTokenMax    = 60

	// СВОЙ бюджет на ip, а не общий с /api/p и /api/pv. Популяции разные: за /api/p сидит один
	// админ, листающий плитки, а за /api/rp — цех, у которого на всю бригаду один выходной NAT, и
	// каждое сканирование стоит манифеста. Общий бюджет означал бы, что раскройный стол тратит
	// анти-скан-запас, заведённый под другую угрозу, — и отказ был бы НЕОТЛИЧИМ от отозванной
	// ссылки (тот же голый 404, причина в Debug), то есть в поле это звучало бы как «QR не
	// работает», и в логах нечем было бы возразить.
	perIPWindow = time.Minute
	perIPMax    = 1200

	statsFlushInterval = time.Minute

	// deniedLogSample — 1 из N отказов пишется на Info, остальные на Debug (см. notFound).
	deniedLogSample = 10
)

// Service минтит токены нарядов и обслуживает /api/rp/{token}.
type Service struct {
	runs   Runs
	cards  Cards
	minter *patterntoken.Minter

	tokenLimiter *ratelimit.Limiter
	ipLimiter    *ratelimit.Limiter

	// Отложенная статистика доступа: аудит — это строка slog на каждый запрос, а счётчики нужны
	// админскому экрану, поэтому они сворачиваются пачкой, а не пишутся по строке на запрос.
	statsMu    sync.Mutex
	statsCount map[int]int64
	statsLast  map[int]time.Time
	stopCh     chan struct{}
	// flushDone закрывается тикером при выходе. Stop ЖДЁТ его прежде чем досбросить остаток:
	// иначе сброс, начавшийся по тику за миг до остановки, продолжал бы писать в базу, которую
	// App.Stop уже закрывает следом (порядок «воркеры раньше БД» держится только если остановка
	// действительно дождалась воркера).
	flushDone chan struct{}
	stopOnce  sync.Once

	// deniedSeq крутит 1-из-N сэмплирование строк отказа.
	deniedSeq atomic.Int64
}

// New собирает сервис. Пустой pepper — отказ на старте (patterntoken.NewMinter): пустой ключ HMAC
// сделал бы любой наряд подделываемым каждым, кто прочитал этот файл.
func New(runs Runs, cards Cards, pepper string) (*Service, error) {
	minter, err := patterntoken.NewMinter(pepper)
	if err != nil {
		return nil, err
	}
	s := &Service{
		runs:         runs,
		cards:        cards,
		minter:       minter,
		tokenLimiter: ratelimit.NewLimiter(perTokenWindow, perTokenMax),
		ipLimiter:    ratelimit.NewLimiter(perIPWindow, perIPMax),
		statsCount:   map[int]int64{},
		statsLast:    map[int]time.Time{},
		stopCh:       make(chan struct{}),
		flushDone:    make(chan struct{}),
	}
	go s.flushLoop()
	return s, nil
}

// Stop останавливает сброс статистики (идемпотентно) и досбрасывает накопленное — дождавшись
// тикера, чтобы последний сброс не пересёкся с закрытием базы.
func (s *Service) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
		<-s.flushDone
		s.flushStats()
		s.tokenLimiter.Stop()
		s.ipLimiter.Stop()
	})
}

func (s *Service) flushLoop() {
	t := time.NewTicker(statsFlushInterval)
	defer t.Stop()
	defer close(s.flushDone)
	for {
		select {
		case <-t.C:
			s.flushStats()
		case <-s.stopCh:
			return
		}
	}
}

func (s *Service) flushStats() {
	s.statsMu.Lock()
	counts := s.statsCount
	last := s.statsLast
	s.statsCount = map[int]int64{}
	s.statsLast = map[int]time.Time{}
	s.statsMu.Unlock()
	if len(counts) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.runs.RecordRunPackAccess(ctx, counts, last); err != nil {
		slog.Default().ErrorContext(ctx, "run pack stats flush failed", slog.String("err", err.Error()))
	}
}

func (s *Service) noteAccess(runID int) {
	s.statsMu.Lock()
	s.statsCount[runID]++
	s.statsLast[runID] = time.Now().UTC()
	s.statsMu.Unlock()
}

// MintRunPackToken отдаёт токен скоупа 'r' для прогона, заводя строку доступа при первом минте
// (epoch 1). Безопасен на nil-получателе (тесты собирают сервер без сервиса) и best effort ровно
// как MintCardViewerToken: сбой минта не имеет права уронить чтение прогона — ответ просто приедет
// без токена, а печать наряда останется без QR.
func (s *Service) MintRunPackToken(ctx context.Context, runID int) string {
	if s == nil || runID <= 0 {
		return ""
	}
	row, err := s.runs.EnsureRunPackAccess(ctx, runID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "mint run pack token failed",
			slog.Int("run_id", runID), slog.String("err", err.Error()))
		return ""
	}
	return s.minter.Mint(patterntoken.ScopeRunPack, int64(runID), row.Epoch)
}

// Handler адаптирует ServeManifest под монтирование в http-сервере.
func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.ServeManifest) }

// ServeManifest обслуживает GET/HEAD /api/rp/{token}. Каждый отрицательный исход — один и тот же
// голый 404, причина только в сэмплированном аудите, ответ некэшируем.
func (s *Service) ServeManifest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := chi.URLParam(r, "token")
	ip := middleware.ClientIPFromRequest(r)

	notFound := func(reason string) {
		// ПРИЧИНА живёт в аудите и никогда в ответе. Отказы сэмплируются на Info (каждый
		// deniedLogSample-й), остальные на Debug: эндпоинт неаутентифицированный, и строка лога на
		// каждый отбитый запрос — усилитель объёма логов, который может дёрнуть кто угодно.
		level := slog.LevelDebug
		if s.deniedSeq.Add(1)%deniedLogSample == 0 {
			level = slog.LevelInfo
		}
		slog.Default().Log(ctx, level, "run pack denied",
			slog.String("reason", reason), slog.String("ip", ip),
			slog.String("ua", r.UserAgent()))
		http.NotFound(w, r)
	}

	if !s.ipLimiter.Allow(ip) {
		notFound("ip rate limited")
		return
	}
	scope, id, epoch, err := s.minter.Parse(token)
	if err != nil {
		notFound("bad token")
		return
	}
	// СКОУП-ALLOWLIST. Здесь id — это номер ПРОГОНА, а токены 'i'/'p' несут id строки
	// pattern_object_access, 'c' — id тех-карты. Все три диапазона пересекаются числами, поэтому
	// принятый чужой скоуп означал бы выдачу наряда партии, у которой просто совпал номер с
	// выкройкой или карточкой. Allowlist, а не denylist: будущий скоуп обязан упереться здесь, а не
	// унаследовать семантику наряда по умолчанию.
	if scope != patterntoken.ScopeRunPack {
		notFound("wrong token scope")
		return
	}
	// Ключ по РАСПАРСЕННОМУ id, а не по строке токена: Parse уже пришпилил каноническое написание,
	// но бюджет — про прогон, а строковый ключ выдал бы новое ведро на каждый вариант написания.
	if !s.tokenLimiter.Allow(ip + "|r|" + strconv.Itoa(int(id))) {
		notFound("token rate limited")
		return
	}
	runID := int(id)
	row, err := s.runs.GetRunPackAccess(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			notFound("no access row")
		} else {
			slog.Default().ErrorContext(ctx, "run pack lookup failed", slog.String("err", err.Error()))
			notFound("lookup error")
		}
		return
	}
	now := time.Now().UTC()
	switch {
	case row.Epoch != epoch:
		notFound("stale epoch")
		return
	case row.RevokedAt.Valid:
		notFound("revoked")
		return
	case row.ExpiresAt.Valid && now.After(row.ExpiresAt.Time):
		notFound("expired")
		return
	}

	m, err := s.buildManifest(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Строка доступа пережила прогон (гонка каскада FK с чтением) — тот же 404.
			notFound("run gone")
		} else {
			slog.Default().ErrorContext(ctx, "run pack manifest read failed", slog.String("err", err.Error()))
			notFound("manifest error")
		}
		return
	}

	slog.Default().InfoContext(ctx, "run pack access",
		slog.Int("run_id", runID), slog.String("ip", ip), slog.String("ua", r.UserAgent()))
	s.noteAccess(runID)

	// Адрес токена стабилен, а содержимое за ним — нет: наряд живой (количества берутся из прогона,
	// а прогон правят) и отражает текущий отзыв. Общим кэшам его хранить нельзя.
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m)
}
