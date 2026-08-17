// Package fileaccess — ПУБЛИЧНАЯ ССЫЛКА НА ФАЙЛ БИБЛИОТЕКИ: минт токена скоупа 'f' для
// админских ответов и обслуживание GET|HEAD /api/f/{token} без JWT.
//
// Посадка списана с публичного наряда на партию (internal/runpackaccess) и с токенизированного
// чтения выкроек (internal/patternaccess), и списана намеренно: свойства, ради которых там всё
// сделано именно так, здесь те же. Токен аутентифицирует САМ СЕБЯ — админской авторизации в
// этом пути нет, потому что ссылку открывает подрядчик, у которого нет аккаунта. Любой отказ
// (битая подпись, чужой скоуп, устаревшее поколение, отзыв, срок, СМЕНА УРОВНЯ, лимит) — один
// и тот же ГОЛЫЙ 404: снаружи «такого файла нет» и «файл закрыли» обязаны быть неразличимы,
// иначе перебор токенов превращается в способ узнать, что файл существует. Причина уходит
// только в аудит и уходит с сэмплированием.
//
// ЧЕГО ЗДЕСЬ НЕТ И НЕ ПОЯВИТСЯ.
//
//  1. ACL БАКЕТА. Объект остаётся приватным навсегда; публичность даёт ЭТОТ маршрут, который
//     на каждое попадание минтит короткоживущий presigned url. Публичный ACL нельзя отозвать
//     сменой уровня — он переживёт и уровень, и сам файл, и любую нашу логику.
//  2. ЧУЖИХ КЛЮЧЕЙ. object_key берётся ТОЛЬКО из строки library_file (entity.LibraryFileLinkTarget),
//     никогда из запроса. Это и есть то, что не даёт подписывателю стать оракулом на
//     произвольный объект бакета.
//  3. INLINE ДЛЯ svg И html. Presigned url смотрит в origin бакета, поэтому отрисованный на
//     месте svg или html исполнил бы скрипты в контексте этого origin. Аллоулист безопасных
//     типов один на всю библиотеку — dto.IsInlineSafeContentType.
package fileaccess

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/patterntoken"
	"github.com/jekabolt/grbpwr-manager/internal/ratelimit"
)

// Files — узкий срез dependency.Files, который нужен публичному маршруту. dependency.Files его
// удовлетворяет; узкий он затем, чтобы тест этого пакета собирался на фейке в двадцать строк, а
// не на моке всей библиотеки — и чтобы из этого пакета физически нельзя было прочитать ни тему,
// ни владельца, ни обсуждение.
type Files interface {
	GetFileByPublicLink(ctx context.Context, fileID int) (*entity.LibraryFileLinkTarget, error)
	RecordPublicAccess(ctx context.Context, counts map[int]int64, last map[int]time.Time) error
}

// Presigner — узкий срез dependency.FileStore. ИМЕННО PresignLibraryObjectShortLived, а не пары
// «подписать что угодно»: у метода свой сегмент-гейт (files-library/), и он единственный
// легитимный способ подписать объект библиотеки для этого маршрута.
//
// И ИМЕННО КОРОТКОЖИВУЩИЙ, А НЕ ОКОННЫЙ PresignLibraryObject. Оконный подписывает на 6–12 часов и
// мемоизирует строку на процесс; выданный им url переживает и «пересоздать», и истёкший срок, и
// смену уровня — то есть ровно те три вещи, которыми этот маршрут закрывается. Отзыв, который
// действует через двенадцать часов, — это не отзыв. Панели мемоизация нужна (эмбеды), здесь она
// не нужна никому: ссылку открывают один раз.
type Presigner interface {
	PresignLibraryObjectShortLived(ctx context.Context, objectKey string, download bool, downloadName string) (string, time.Time, error)
}

const (
	// Пер (ip|файл). Ссылку открывают, перезагружают и тянут второй раз ради скачивания —
	// бюджету достаточно покрывать это.
	perTokenWindow = time.Minute
	perTokenMax    = 60

	// СВОЙ бюджет на ip, отдельный от /api/p, /api/pv и /api/rp. Популяции разные: здесь за
	// ссылкой сидит человек ВНЕ компании, которому её прислали, а общий бюджет означал бы, что
	// подрядчик тратит анти-скан-запас, заведённый под другую угрозу. Отказ по лимиту — тот же
	// голый 404 с причиной в Debug, то есть в поле это звучало бы как «ссылка не работает», и
	// в логах нечем было бы возразить.
	perIPWindow = time.Minute
	perIPMax    = 600

	statsFlushInterval = time.Minute

	// deniedLogSample — 1 из N отказов пишется на Info, остальные на Debug (см. notFound).
	deniedLogSample = 10

	// maxPendingFiles — потолок числа РАЗЛИЧНЫХ файлов в неотправленной пачке статистики.
	// Счётчики уже накопленных файлов растут дальше, а вот число ключей растёт вместе с
	// длительностью аварии базы и ничем сверху не ограничено. Тот же размен, что в
	// runpackaccess: статистика теряется громко, процесс живёт.
	maxPendingFiles = 4096
)

// Service минтит публичные ссылки и обслуживает /api/f/{token}.
type Service struct {
	files   Files
	presign Presigner
	minter  *patterntoken.Minter

	// baseURL — внешний origin этого бэкенда (PatternToken.PublicBaseURL без хвостового слэша).
	// Ссылка АБСОЛЮТНАЯ, потому что её копируют в мессенджер, а не открывают из панели.
	baseURL string

	tokenLimiter *ratelimit.Limiter
	ipLimiter    *ratelimit.Limiter

	// Отложенная статистика: маршрут публичный, и запись строки в базу на каждый заход — это
	// способ уронить базу чужими руками. Аудит — это строка slog на попадание, а счётчики
	// сворачиваются пачкой раз в минуту.
	statsMu    sync.Mutex
	statsCount map[int]int64
	statsLast  map[int]time.Time
	stopCh     chan struct{}
	// flushDone закрывается тикером при выходе; Stop ЖДЁТ его прежде чем досбросить остаток,
	// иначе сброс по тику продолжал бы писать в базу, которую App.Stop закрывает следом.
	flushDone chan struct{}
	stopOnce  sync.Once

	// deniedSeq крутит 1-из-N сэмплирование строк отказа.
	deniedSeq atomic.Int64
}

// New собирает сервис. Пустой pepper — отказ на старте (patterntoken.NewMinter): пустой ключ
// HMAC сделал бы подделываемой каждую ссылку каждым, кто прочитал этот файл.
func New(files Files, presign Presigner, pepper, baseURL string) (*Service, error) {
	minter, err := patterntoken.NewMinter(pepper)
	if err != nil {
		return nil, err
	}
	s := &Service{
		files:        files,
		presign:      presign,
		minter:       minter,
		baseURL:      strings.TrimRight(baseURL, "/"),
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

// LinkURL отдаёт публичный адрес файла для поколения epoch. Безопасен на nil-получателе
// (тесты и часть сборок админского сервера живут без сервиса) — тогда блок доступа приезжает
// без url, а не падает: ссылка это свойство ответа, а не условие его существования.
//
// ДЕТЕРМИНИРОВАН: одно поколение одного файла всегда даёт одну строку. Именно поэтому
// «пересоздать» — это +1 к поколению, а не выпуск второй ссылки рядом.
func (s *Service) LinkURL(fileID, epoch int) string {
	if s == nil || fileID <= 0 {
		return ""
	}
	return s.baseURL + "/api/f/" + s.minter.Mint(patterntoken.ScopeFile, int64(fileID), epoch)
}

// Handler адаптирует ServeFile под монтирование в http-сервере.
func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.ServeFile) }

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

// flushStats сбрасывает накопленное в базу и ВОЗВРАЩАЕТ пачку в pending при неудаче: счётчик и
// время последнего обращения — единственный ответ на вопрос «этой ссылкой вообще пользуются»,
// и молча потерянная минута читается как «не пользуются».
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
	if err := s.files.RecordPublicAccess(ctx, counts, last); err != nil {
		slog.Default().ErrorContext(ctx, "library file public access stats flush failed",
			slog.String("err", err.Error()))
		s.returnUnflushed(counts, last)
	}
}

// returnUnflushed СКЛАДЫВАЕТ несохранённую пачку с тем, что накопилось за время записи, а не
// присваивает: пока шла запись, noteAccess уже клал новые попадания в свежие карты, и
// присвоение затёрло бы их ровно так же, как их затирала бы потерянная пачка. Время последнего
// обращения берётся МАКСИМУМОМ по той же причине.
func (s *Service) returnUnflushed(counts map[int]int64, last map[int]time.Time) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	dropped := 0
	for id, n := range counts {
		if _, ok := s.statsCount[id]; !ok && len(s.statsCount) >= maxPendingFiles {
			dropped++
			continue
		}
		s.statsCount[id] += n
		if t, ok := last[id]; ok {
			if cur, seen := s.statsLast[id]; !seen || t.After(cur) {
				s.statsLast[id] = t
			}
		}
	}
	if dropped > 0 {
		slog.Default().Error("library file public access stats pending overflow, counters dropped",
			slog.Int("dropped_files", dropped), slog.Int("pending_files", len(s.statsCount)))
	}
}

func (s *Service) noteAccess(fileID int) {
	s.statsMu.Lock()
	s.statsCount[fileID]++
	s.statsLast[fileID] = time.Now().UTC()
	s.statsMu.Unlock()
}

// ServeFile обслуживает GET|HEAD /api/f/{token}: 302 на короткоживущий presigned url, либо
// метаданные при ?mode=json. Каждый отрицательный исход — один и тот же голый 404.
func (s *Service) ServeFile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := chi.URLParam(r, "token")
	ip := middleware.ClientIPFromRequest(r)

	notFound := func(reason string) {
		// ПРИЧИНА живёт в аудите и никогда в ответе. Отказы сэмплируются на Info (каждый
		// deniedLogSample-й), остальные на Debug: эндпоинт неаутентифицированный, и строка
		// лога на каждый отбитый запрос — усилитель объёма логов, который может дёрнуть кто
		// угодно из интернета.
		level := slog.LevelDebug
		if s.deniedSeq.Add(1)%deniedLogSample == 0 {
			level = slog.LevelInfo
		}
		slog.Default().Log(ctx, level, "library file link denied",
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
	// СКОУП-ALLOWLIST. Здесь id — это id ФАЙЛА, а токены 'i'/'p' несут id строки
	// pattern_object_access, 'c' — id тех-карты, 'r' — id прогона. Все четыре пространства
	// пересекаются числами, поэтому принятый чужой скоуп означал бы выдачу файла, у которого
	// просто совпал номер с выкройкой, карточкой или партией. Allowlist, а не denylist:
	// будущий скоуп обязан упереться здесь, а не унаследовать семантику файла по умолчанию.
	if scope != patterntoken.ScopeFile {
		notFound("wrong token scope")
		return
	}
	// Ключ по РАСПАРСЕННОМУ id: Parse уже пришпилил каноническое написание, но бюджет — про
	// файл, а строковый ключ выдал бы новое ведро на каждый вариант написания токена.
	if !s.tokenLimiter.Allow(ip + "|f|" + strconv.Itoa(int(id))) {
		notFound("token rate limited")
		return
	}
	fileID := int(id)
	row, err := s.files.GetFileByPublicLink(ctx, fileID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Ни файла, ни строки доступа — снаружи это одно и то же «нет такого».
			notFound("no access row")
		} else {
			slog.Default().ErrorContext(ctx, "library file link lookup failed", slog.String("err", err.Error()))
			notFound("lookup error")
		}
		return
	}
	now := time.Now().UTC()
	switch {
	case row.AccessLevel != entity.LibraryFileAccessLink:
		// ГЛАВНАЯ ПРОВЕРКА ЭТОГО МАРШРУТА, и она на строке ФАЙЛА, а не на наличии строки
		// доступа: строка доступа переживает уровень, поэтому файл, переключённый обратно в
		// `team`, обязан быть мёртв по ссылке ПРИ ЛЮБОМ совпадении поколения.
		notFound("level is not link")
		return
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

	// INLINE ТОЛЬКО БЕЗОПАСНЫМ ТИПАМ. Всё остальное — включая svg и html — уходит вложением,
	// сколько бы раз ни просили обратное: presigned url смотрит в origin бакета, и
	// отрисованный на месте документ исполнил бы скрипты в его контексте. `?dl=1` может
	// сделать вложением безопасный тип, но не может сделать inline небезопасный.
	download := r.URL.Query().Get("dl") == "1" || !dto.IsInlineSafeContentType(row.ContentType)
	// Имя вложения — ТОЛЬКО из строки базы: оно приземляется в заголовок ответа, и параметр
	// запроса на его месте был бы инъекцией в Content-Disposition.
	//
	// Подпись КОРОТКОЖИВУЩАЯ и не мемоизированная (см. Presigner): за этой строкой стоит человек
	// вне компании, и единственное, чем его можно отключить, — истечение подписи. Всё остальное
	// (поколение, срок, уровень) проверяется ЗДЕСЬ и на уже выданный bucket-url не действует.
	signed, expiresAt, err := s.presign.PresignLibraryObjectShortLived(ctx, row.ObjectKey, download, row.FileName)
	if err != nil {
		slog.Default().ErrorContext(ctx, "library file presign failed", slog.String("err", err.Error()))
		notFound("presign error")
		return
	}

	slog.Default().InfoContext(ctx, "library file link access",
		slog.Int("file_id", fileID), slog.String("ip", ip),
		slog.String("ua", r.UserAgent()), slog.Bool("dl", download))
	s.noteAccess(fileID)

	// Адрес токена стабилен, а то, что за ним, — нет: файл отзывают, переключают уровень и
	// пересоздают ссылку. Общим кэшам его хранить нельзя.
	w.Header().Set("Cache-Control", "private, no-store")
	if r.URL.Query().Get("mode") == "json" {
		// Для страницы приземления: она рисует имя, тип и размер и только потом ведёт к
		// байтам — а ещё это единственный способ отдать presigned url тому, кто тянет файл
		// через fetch (кросс-доменный редирект обнуляет origin и ломает CORS бакета).
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"url":          signed,
			"expires_at":   expiresAt.Format(time.RFC3339),
			"file_name":    row.FileName,
			"content_type": row.ContentType,
			"size_bytes":   row.SizeBytes,
			"download":     download,
		})
		return
	}
	http.Redirect(w, r, signed, http.StatusFound)
}
