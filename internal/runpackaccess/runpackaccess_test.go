package runpackaccess

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/patterntoken"
	"github.com/shopspring/decimal"
)

const testPepper = "test-pepper-for-run-pack"

// moneySentinels — суммы, которыми фикстура насыщает прогон и карту. Ни одна из них не имеет права
// появиться в манифесте; ищутся они в сыром JSON, потому что число переживает переименование поля.
var moneySentinels = []string{"777.77", "888.88", "999.99", "555.55", "444.44"}

// serveRunPack routes через тот же mount, что и http.go, чтобы chi.URLParam видел токен так же,
// как в бою (включая HEAD — отказ по методу обязан быть неотличим от отказа по токену).
func serveRunPack(svc *Service, method, target string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Method(http.MethodGet, "/api/rp/{token}", svc.Handler())
	r.Method(http.MethodHead, "/api/rp/{token}", svc.Handler())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

// fakeRuns — узкий фейк вместо мока всего репозитория партий: интерфейс Runs ровно за этим и узкий.
type fakeRuns struct {
	access    map[int]*entity.ProductionRunPackAccess
	pack      *entity.RunPack
	lays      entity.ProductionRunLayList
	comp      map[int][]entity.RunPackMarkerSize
	ensured   int
	recorded  map[int]int64
	packError error
}

func (f *fakeRuns) EnsureRunPackAccess(_ context.Context, runID int) (*entity.ProductionRunPackAccess, error) {
	f.ensured++
	if row, ok := f.access[runID]; ok {
		return row, nil
	}
	row := &entity.ProductionRunPackAccess{RunId: runID, Epoch: 1}
	f.access[runID] = row
	return row, nil
}

func (f *fakeRuns) GetRunPackAccess(_ context.Context, runID int) (*entity.ProductionRunPackAccess, error) {
	if row, ok := f.access[runID]; ok {
		return row, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeRuns) RecordRunPackAccess(_ context.Context, counts map[int]int64, _ map[int]time.Time) error {
	if f.recorded == nil {
		f.recorded = map[int]int64{}
	}
	for id, n := range counts {
		f.recorded[id] += n
	}
	return nil
}

func (f *fakeRuns) GetRunPack(_ context.Context, runID int) (*entity.RunPack, error) {
	if f.packError != nil {
		return nil, f.packError
	}
	if f.pack == nil || f.pack.Run.Id != runID {
		return nil, sql.ErrNoRows
	}
	return f.pack, nil
}

func (f *fakeRuns) GetRunPackMarkerSizes(_ context.Context, ids []int) (map[int][]entity.RunPackMarkerSize, error) {
	out := map[int][]entity.RunPackMarkerSize{}
	for _, id := range ids {
		if rows, ok := f.comp[id]; ok {
			out[id] = rows
		}
	}
	return out, nil
}

func (f *fakeRuns) ListLays(_ context.Context, _ int) (entity.ProductionRunLayList, error) {
	return f.lays, nil
}

type fakeCards struct{ card *entity.TechCard }

func (f *fakeCards) GetTechCardById(_ context.Context, _ int) (*entity.TechCard, error) {
	if f.card == nil {
		return nil, sql.ErrNoRows
	}
	return f.card, nil
}

func nd(v string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
}

func ni32(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }
func ni64(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
func ns(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
func nt(t time.Time) sql.NullTime {
	return sql.NullTime{Time: t, Valid: true}
}

// fixture собирает прогон из двух колорвеев на двух размерах, карту с непривязанным слотом (даёт
// блокер материального плана) и один настил с составом раскладки.
func fixture() (*fakeRuns, *fakeCards) {
	run := entity.ProductionRun{
		Id:          41,
		LockVersion: 7,
		ProductionRunInsert: entity.ProductionRunInsert{
			TechCardId: 5,
			ReleaseId:  ni64(12),
			Status:     entity.ProductionRunInProgress,
			// Деньги на самом прогоне: узкое чтение их не грузит, но фикстура ставит их нарочно —
			// тест обязан проверять сборщик манифеста, а не удачу чтения.
			PlannedUnitCost: nd("444.44"),
			PlannedCurrency: ns("EUR"),
			PlannedStartAt:  nt(time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)),
			PromisedAt:      nt(time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)),
		},
	}
	lines := []entity.RunPackLine{
		{ProductionRunLine: entity.ProductionRunLine{ProductId: ni32(55), SizeId: 8, PlannedQty: 20},
			ColorwayName: "Чёрный", SizeName: "s"},
		{ProductionRunLine: entity.ProductionRunLine{ProductId: ni32(55), SizeId: 7, PlannedQty: 10},
			ColorwayName: "Чёрный", SizeName: "xs"},
		{ProductionRunLine: entity.ProductionRunLine{ProductId: ni32(66), SizeId: 7, PlannedQty: 5},
			ColorwayName: "Синий", SizeName: "xs"},
		// Размер, выпавший из градации карты уже после планирования: он обязан приехать в наряд
		// (за ним стоит реальное плановое количество), но не встать в середину ряда.
		{ProductionRunLine: entity.ProductionRunLine{ProductId: ni32(66), SizeId: 9, PlannedQty: 3},
			ColorwayName: "Синий", SizeName: "xxl"},
		// Вторая строка на ту же пару (колорвей, размер): грид прогона это допускает, и наряд
		// обязан показать ОДНУ клетку с суммой, а не две колонки одного размера.
		{ProductionRunLine: entity.ProductionRunLine{ProductId: ni32(55), SizeId: 8, PlannedQty: 6},
			ColorwayName: "Чёрный", SizeName: "s"},
	}
	for i := range lines {
		run.Lines = append(run.Lines, lines[i].ProductionRunLine)
	}

	runs := &fakeRuns{
		access: map[int]*entity.ProductionRunPackAccess{},
		pack: &entity.RunPack{
			Run:           run,
			Lines:         lines,
			StyleNumber:   "GR-042",
			StyleName:     "hooded jacket",
			FactoryName:   "Фабрика №1",
			ReleaseNumber: 3,
			ReleasedAt:    nt(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)),
		},
		lays: entity.ProductionRunLayList{
			Applicable: true,
			Lays: []entity.ProductionRunLay{{
				Id: 1, RunId: 41, Name: "Основной настил", ColorwayId: 55, ColorwayName: "Чёрный",
				BomLineKey: "01LINEMAIN0000000000000000", BomItemName: ns("Основная ткань"),
				Mode: entity.ProductionLayModeFaceToFace,
				Sections: []entity.ProductionRunLaySection{
					{MarkerId: 301, Plies: 24, MarkerName: "S+M"},
				},
			}},
		},
		comp: map[int][]entity.RunPackMarkerSize{
			301: {{MarkerId: 301, SizeId: 7, SizeName: "xs", Quantity: 1},
				{MarkerId: 301, SizeId: 8, SizeName: "s", Quantity: 2}},
		},
	}

	// Карта нарочно НАСЫЩЕНА деньгами, и суммы выбраны узнаваемыми (moneySentinels): целая
	// прайсовая карта — это ВХОД проекций, и единственный способ доказать, что она не протекает в
	// цех, — искать в готовом JSON сами эти числа, а не слово «price».
	card := &entity.TechCard{Id: 5}
	card.SizeIds = []int{7, 8}
	card.BomItems = []entity.TechCardBomItem{
		{Name: "Основная ткань", MaterialId: ni64(100), Unit: ns("m"), WastagePercent: nd("5"),
			UnitPrice: nd("777.77"), Currency: ns("EUR")},
		// Слот без артикула, на который ссылается рецепт колорвея → блокер «нет артикула».
		{Name: "Тесьма", Unit: ns("pc"), UnitPrice: nd("888.88")},
	}
	card.Colorways = []entity.TechCardColorway{
		{Id: 1, Name: "Чёрный", ProductId: ni32(55), CostPrice: nd("999.99"),
			Usages: []entity.TechCardColorwayUsage{
				{BomItemIndex: ni32(0), Consumption: nd("2")},
				{BomItemIndex: ni32(1), Quantity: nd("3")},
			}},
		{Id: 2, Name: "Синий", ProductId: ni32(66), Usages: []entity.TechCardColorwayUsage{
			{BomItemIndex: ni32(0), Consumption: nd("2")},
		}},
	}
	card.LinkedMaterials = map[int]entity.MaterialWithPrice{
		100: {LatestPrice: &entity.MaterialPrice{MaterialId: 100,
			Price: decimal.RequireFromString("555.55"), Currency: "EUR"}},
	}
	return runs, &fakeCards{card: card}
}

func newTestService(t *testing.T) (*Service, *fakeRuns, *fakeCards) {
	t.Helper()
	runs, cards := fixture()
	svc, err := New(runs, cards, testPepper)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(svc.Stop)
	return svc, runs, cards
}

// TestMintReadRevoke — жизненный цикл капабилити: минт заводит строку доступа, токен открывает
// манифест без всякой аутентификации, отзыв делает ту же ссылку неотличимой от несуществующей.
func TestMintReadRevoke(t *testing.T) {
	svc, runs, _ := newTestService(t)

	token := svc.MintRunPackToken(context.Background(), 41)
	if token == "" || token[0] != byte(patterntoken.ScopeRunPack) {
		t.Fatalf("mint returned %q, want a non-empty 'r'-scoped token", token)
	}
	if runs.access[41] == nil {
		t.Fatal("mint must create the access row lazily")
	}
	if again := svc.MintRunPackToken(context.Background(), 41); again != token {
		t.Fatalf("mint is not deterministic: %q vs %q", token, again)
	}

	w := serveRunPack(svc, http.MethodGet, "/api/rp/"+token)
	if w.Code != http.StatusOK {
		t.Fatalf("valid token: got %d, want 200", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q; a live document must not be cached by shared caches", got)
	}

	// Отзыв: та же ссылка, тот же epoch, но строка выключена.
	runs.access[41].RevokedAt = nt(time.Now().UTC())
	if w := serveRunPack(svc, http.MethodGet, "/api/rp/"+token); w.Code != http.StatusNotFound {
		t.Fatalf("revoked token: got %d, want 404", w.Code)
	}

	// Bump epoch — второй способ отзыва: напечатанная бумага несёт старый epoch.
	runs.access[41].RevokedAt = sql.NullTime{}
	runs.access[41].Epoch++
	if w := serveRunPack(svc, http.MethodGet, "/api/rp/"+token); w.Code != http.StatusNotFound {
		t.Fatalf("stale epoch: got %d, want 404", w.Code)
	}
}

// TestExpiredIsNotFound — протухание проверяет обработчик по КОЛОНКЕ, а не по содержимому токена:
// политику можно поменять, не перепечатывая QR.
func TestExpiredIsNotFound(t *testing.T) {
	svc, runs, _ := newTestService(t)
	token := svc.MintRunPackToken(context.Background(), 41)
	runs.access[41].ExpiresAt = nt(time.Now().UTC().Add(-time.Minute))
	if w := serveRunPack(svc, http.MethodGet, "/api/rp/"+token); w.Code != http.StatusNotFound {
		t.Fatalf("expired token: got %d, want 404", w.Code)
	}
}

// TestForeignScopeIsNotFound — главный тест этого пакета. Токены всех скоупов адресуют объект
// ЧИСЛОМ, и числа пересекаются: карточный токен на id 41 подписан валидно, но означает тех-карту
// №41, а не прогон №41. Принять его здесь — это выдать чужой документ по настоящей подписи.
func TestForeignScopeIsNotFound(t *testing.T) {
	svc, runs, _ := newTestService(t)
	svc.MintRunPackToken(context.Background(), 41)
	epoch := runs.access[41].Epoch

	minter, err := patterntoken.NewMinter(testPepper)
	if err != nil {
		t.Fatalf("NewMinter: %v", err)
	}
	for _, scope := range []patterntoken.Scope{
		patterntoken.ScopeCard, patterntoken.ScopeInternal, patterntoken.ScopePrint,
	} {
		foreign := minter.Mint(scope, 41, epoch)
		if w := serveRunPack(svc, http.MethodGet, "/api/rp/"+foreign); w.Code != http.StatusNotFound {
			t.Fatalf("scope %q on the run pack endpoint: got %d, want 404", string(rune(scope)), w.Code)
		}
	}
}

// TestBrokenTokensAreNotFound — подделка, мусор и чужой прогон отдают ОДИН И ТОТ ЖЕ голый 404:
// перебирающий не должен уметь отличить «нет такого» от «отозван».
func TestBrokenTokensAreNotFound(t *testing.T) {
	svc, _, _ := newTestService(t)
	good := svc.MintRunPackToken(context.Background(), 41)

	tampered := good[:len(good)-1] + string(flipLast(good))
	cases := map[string]string{
		"garbage":       "not-a-token",
		"empty-ish":     "r",
		"tampered sig":  tampered,
		"unknown scope": "z1.1.AAAAAAAAAAAAAAAAAAAAAA",
		// Валидная подпись на прогон, у которого нет строки доступа: 404, а не «нет строки».
		"no access row": mustMint(t, patterntoken.ScopeRunPack, 42, 1),
	}
	for name, token := range cases {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			if w := serveRunPack(svc, method, "/api/rp/"+token); w.Code != http.StatusNotFound {
				t.Fatalf("%s (%s): got %d, want 404", name, method, w.Code)
			}
		}
	}
}

func flipLast(s string) rune {
	last := s[len(s)-1]
	if last == 'A' {
		return 'B'
	}
	return 'A'
}

func mustMint(t *testing.T, scope patterntoken.Scope, id int64, epoch int) string {
	t.Helper()
	m, err := patterntoken.NewMinter(testPepper)
	if err != nil {
		t.Fatalf("NewMinter: %v", err)
	}
	return m.Mint(scope, id, epoch)
}

// TestManifestShape — что именно уезжает в цех: шапка, размерная ось в порядке градации (выпавший
// размер в хвосте), матрица с плановыми количествами и краткие настилы с составом раскладки.
func TestManifestShape(t *testing.T) {
	svc, _, _ := newTestService(t)
	token := svc.MintRunPackToken(context.Background(), 41)

	w := serveRunPack(svc, http.MethodGet, "/api/rp/"+token)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	var m Manifest
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest is not json: %v", err)
	}

	if m.RunId != 41 || m.StyleNumber != "GR-042" || m.Factory != "Фабрика №1" {
		t.Fatalf("header: %+v", m)
	}
	if m.ReleaseId != 12 || m.ReleaseNumber != 3 {
		t.Fatalf("release: id=%d number=%d, want 12/3 — цех обязан видеть, по какой ревизии кроит", m.ReleaseId, m.ReleaseNumber)
	}
	if m.RunLockVersion != 7 {
		t.Fatalf("run_lock_version = %d, want 7 (бумага обязана называть свою версию)", m.RunLockVersion)
	}
	if m.Status != string(entity.ProductionRunInProgress) || m.PlannedStartAt == "" || m.PromisedAt == "" {
		t.Fatalf("status/dates: %+v", m)
	}
	if m.GeneratedAt == "" {
		t.Fatal("generated_at must be stamped")
	}

	wantAxis := []int{7, 8, 9}
	if len(m.Sizes) != len(wantAxis) {
		t.Fatalf("size axis = %+v, want ids %v", m.Sizes, wantAxis)
	}
	for i, id := range wantAxis {
		if m.Sizes[i].Id != id {
			t.Fatalf("size axis = %+v, want %v (градация карты, выпавший размер в хвосте)", m.Sizes, wantAxis)
		}
	}
	if m.Sizes[2].Name != "xxl" {
		t.Fatalf("выпавший из градации размер приехал без имени: %+v", m.Sizes[2])
	}

	if len(m.Lines) != 2 || m.GarmentsTotal != 44 {
		t.Fatalf("matrix = %+v, total = %d, want 2 lines / 44 garments", m.Lines, m.GarmentsTotal)
	}
	// 20 + 10 + 6: две строки прогона на пару (55, размер 8) свёрнуты в ОДНУ клетку.
	if m.Lines[0].ColorwayId != 55 || m.Lines[0].PlannedTotal != 36 || len(m.Lines[0].BySize) != 2 {
		t.Fatalf("line 0 = %+v (клетки одной пары обязаны складываться, а не дублироваться)", m.Lines[0])
	}
	if m.Lines[0].BySize[1].SizeId != 8 || m.Lines[0].BySize[1].PlannedQty != 26 {
		t.Fatalf("клетка (55, s) = %+v, want planned_qty 26", m.Lines[0].BySize[1])
	}
	// Клетки строки идут по ТОЙ ЖЕ оси, что и заголовок (в фикстуре линии заведены в обратном
	// порядке нарочно): разъехавшиеся колонки грида — это партия, сшитая не в тех размерах.
	if m.Lines[0].BySize[0].SizeId != 7 || m.Lines[0].BySize[1].SizeId != 8 {
		t.Fatalf("клетки строки не выстроены по размерной оси: %+v", m.Lines[0].BySize)
	}

	if !m.LaysApplicable || len(m.Lays) != 1 {
		t.Fatalf("lays = %+v (applicable=%v)", m.Lays, m.LaysApplicable)
	}
	lay := m.Lays[0]
	if lay.SlotName != "Основная ткань" || lay.TotalPlies != 24 || lay.Mode != string(entity.ProductionLayModeFaceToFace) {
		t.Fatalf("lay = %+v", lay)
	}
	if len(lay.Sections) != 1 || len(lay.Sections[0].Sizes) != 2 || lay.Sections[0].Sizes[1].GarmentsPerPly != 2 {
		t.Fatalf("lay sections = %+v (состав раскладки: изделий на ОДИН слой)", lay.Sections)
	}

	// Блокер материального плана: слот, на который рецепт ссылается, но артикула у него нет.
	if len(m.MaterialBlockers) != 1 || m.MaterialBlockers[0].SlotName != "Тесьма" {
		t.Fatalf("material blockers = %+v", m.MaterialBlockers)
	}
	if m.MaterialBlockers[0].Key == "" {
		t.Fatal("блокер обязан нести стабильный машинный key, а не только фразу")
	}

	// Кат-лист сейчас пуст: dto.ComputeProductionRunCutPlan — заглушка, её тело пишется отдельно.
	// Тест проверяет ТРАНСПОРТ (поля есть, они не nil в JSON), а не начинку.
	if m.CutList == nil || m.CutBlockers == nil || m.CutCaveats == nil {
		t.Fatalf("cut list transport must serialise as [] and never as null: %+v", m)
	}
}

// TestManifestCarriesNoMoney — то, ради чего этот документ вообще собирают руками. Публичный
// эндпоинт не имеет RBAC, которым можно срезать костинг, поэтому денег в нём быть не должно
// НИКАКИХ: ни план-костов прогона, ни цен артикулов, ни актуалов, ни стоимости материала.
// Проверка идёт по СЫРОМУ JSON, а не по структурам: она обязана поймать и поле, которое кто-то
// добавит завтра во вложенную структуру.
func TestManifestCarriesNoMoney(t *testing.T) {
	svc, _, _ := newTestService(t)
	token := svc.MintRunPackToken(context.Background(), 41)
	w := serveRunPack(svc, http.MethodGet, "/api/rp/"+token)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	body := strings.ToLower(w.Body.String())
	// Половина первая: имена полей. Ловит поле, названное деньгами.
	for _, forbidden := range []string{
		"price", "cost", "currency", "amount", "money", "vat", "eur", "margin", "unit_cost",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("манифест наряда содержит %q — деньги в цех не уезжают ни в каком виде:\n%s",
				forbidden, w.Body.String())
		}
	}
	// Половина вторая, и она важнее: САМИ СУММЫ, которыми насыщены прогон и карта на входе. Имя
	// поля можно переназвать (или спрятать сумму в человеческую фразу блокера), а число — нет.
	for _, sentinel := range moneySentinels {
		if strings.Contains(body, sentinel) {
			t.Fatalf("в манифест просочилась сумма %s — целая прайсовая карта на входе обязана "+
				"оставаться входом:\n%s", sentinel, w.Body.String())
		}
	}
}

// TestRateLimitedLooksExactlyLikeEverythingElse — исчерпанный бюджет обязан выглядеть тем же голым
// 404. Отдельный код (429) рассказал бы перебирающему, что токен настоящий.
func TestRateLimitedLooksExactlyLikeEverythingElse(t *testing.T) {
	svc, _, _ := newTestService(t)
	token := svc.MintRunPackToken(context.Background(), 41)
	var last int
	for i := 0; i < perTokenMax+2; i++ {
		last = serveRunPack(svc, http.MethodGet, "/api/rp/"+token).Code
	}
	if last != http.StatusNotFound {
		t.Fatalf("after exhausting the per-token budget: got %d, want 404", last)
	}
}

// TestMintIsBestEffort — сбой минта не имеет права уронить чтение прогона: ответ просто приезжает
// без токена. Nil-получатель — тот же контракт для сервера, собранного без сервиса.
func TestMintIsBestEffort(t *testing.T) {
	var nilSvc *Service
	if got := nilSvc.MintRunPackToken(context.Background(), 41); got != "" {
		t.Fatalf("nil service minted %q", got)
	}
	svc, _, _ := newTestService(t)
	if got := svc.MintRunPackToken(context.Background(), 0); got != "" {
		t.Fatalf("run id 0 minted %q", got)
	}
}
