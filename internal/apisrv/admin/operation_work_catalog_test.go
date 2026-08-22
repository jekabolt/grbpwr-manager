package admin

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"
	"unicode"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ЦИТАТЫ И МУТАЦИИ RPC КАТАЛОГА РАБОТ (R3).
//
// ⚠️ МУТАЦИИ, КОТОРЫМИ ЭТОТ ФАЙЛ ПРОВЕРЕН (прогнаны и откачены):
//
//  1. В ConvertOperationWorkCatalogToPb перестал переноситься `Syn` (сборка каталога сломана
//     ровно в том месте, где ломается тихо: пункты на месте, поиск по русскому слову мёртв).
//     ПОКРАСНЕЛ TestOperationWorkCatalogIsDeliveredWhole — он требует у каждой из 53 работ
//     кириллический синоним, а не только наличия пунктов. Вернул — зелёный.
//  2. Там же перестал переноситься `Machines`: покраснел тот же тест на проверке машинок.
//     Вернул — зелёный.
//  3. Из RememberOperationWorkDefault убрана проверка `entity.ValidateOperationWorkDefaultField`
//     (снято ПРАВИЛО РЕЕСТРА на входе RPC): покраснели
//     TestRememberOperationWorkDefaultRefusesMachineSetting и
//     TestRememberOperationWorkDefaultClearAlsoPassesTheRegistry — снятие машинной настройки доехало
//     до хранилища, то есть завело второй механизм поверх лестницы 0306. ⚠️ Отказ на ЗАПИСЬ при этом
//     не исчез, а СОВРАЛ: сработала защитная ветка проверки значения, и человек услышал «нет такого
//     поля» вместо «это живёт в парке оборудования» — то есть пошёл бы искать опечатку. Ровно поэтому
//     тест сверяет ПРИЧИНУ и текст, а не факт отказа. Вернул — зелёный.
//  4. В GetOperationWorkCatalog подсказки перестали пропускаться через
//     entity.LatestOperationWorkSmv (сырые образцы поехали как есть): покраснел
//     TestOperationWorkSmvHintComesFromTheLatestStep — на одну работу приехали две подсказки,
//     причём первой старая. Вернул — зелёный.
//
// ПОЧЕМУ ФИКСТУРА ЧИТАЕТСЯ ИЗ САМОЙ МИГРАЦИИ, А НЕ ВЫПИСАНА ЗДЕСЬ ДВУМЯ ПУНКТАМИ. Цитата гейта —
// «каталог отдаётся ЦЕЛИКОМ (53 работы, синонимы, машинки, дефолты) и переживает круг», и на
// фикстуре из двух работ она не цитата, а обещание: перенос, который теряет одно поле у одной
// работы из полусотни, на двух пунктах не виден. Файл 0329 — единственный источник каталога, и
// правила над ним держит migrationlint; здесь он читается ради ДОСТАВКИ, а не ради правил, так что
// двух источников правды не возникает — источник один, читателей у него два.

const workSeedFile = "0329_operation_work_catalog.sql"

// seededWorks parses the work rows of migration 0329 into catalog entities, exactly as the store
// would have returned them.
func seededWorks(t *testing.T) []entity.OperationWork {
	t.Helper()
	path := filepath.Join("..", "..", "store", "sql", workSeedFile)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("не читается сид каталога %s: %v", path, err)
	}
	content := string(body)

	workRe := regexp.MustCompile(
		`(?m)^\s*\('([a-z0-9_]+)', '([a-z0-9_]+)', '([a-z0-9_]+)', '([^']*)', '([a-z]+)', (?:'([a-z0-9_]+)'|NULL), (\d+)\),?$`)
	pairRe := regexp.MustCompile(`(?m)^\s*\('([a-z0-9_]+)', '([^']+)'\),?$`)

	section := func(header string) string {
		start := indexAfter(t, content, header)
		end := indexFrom(t, content[start:], "ON DUPLICATE KEY UPDATE")
		return content[start : start+end]
	}

	var works []entity.OperationWork
	byToken := map[string]*entity.OperationWork{}
	for _, m := range workRe.FindAllStringSubmatch(
		section("INSERT INTO operation_work (token, verb, stage, label, machine_mode, default_machine, sort) VALUES"), -1) {
		sortN, err := strconv.Atoi(m[7])
		if err != nil {
			t.Fatalf("sort не число: %v", err)
		}
		works = append(works, entity.OperationWork{
			Token: m[1], Verb: m[2], Stage: m[3], Label: m[4], MachineMode: m[5],
			DefaultMachine: sql.NullString{String: m[6], Valid: m[6] != ""},
			Sort:           sortN,
		})
	}
	if len(works) == 0 {
		t.Fatalf("%s: сид работ не разобрался — тест перестал что-либо доказывать", workSeedFile)
	}
	for i := range works {
		byToken[works[i].Token] = &works[i]
	}
	for _, m := range pairRe.FindAllStringSubmatch(
		section("INSERT INTO operation_work_machine (work_token, machine_type) VALUES"), -1) {
		if w := byToken[m[1]]; w != nil {
			w.Machines = append(w.Machines, m[2])
		}
	}
	for _, m := range pairRe.FindAllStringSubmatch(
		section("INSERT INTO operation_work_syn (work_token, syn) VALUES"), -1) {
		if w := byToken[m[1]]; w != nil {
			w.Syn = append(w.Syn, m[2])
		}
	}
	return works
}

func indexAfter(t *testing.T, s, sub string) int {
	t.Helper()
	i := indexFrom(t, s, sub)
	return i + len(sub)
}

func indexFrom(t *testing.T, s, sub string) int {
	t.Helper()
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	t.Fatalf("%s: не найден фрагмент %q", workSeedFile, sub)
	return 0
}

func hasCyrillicRune(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// TestOperationWorkCatalogIsDeliveredWhole — ЦИТАТА: каталог переживает круг ЦЕЛИКОМ.
//
// Пятьдесят три работы сида, у каждой её ярлык, стадия, режим машинки, машинки и оба сорта слов
// поиска; плюс дефолты и реестр полей. Ни одно поле не «наверное доедет» — каждое сверяется со
// строкой, из которой оно пришло.
func TestOperationWorkCatalogIsDeliveredWhole(t *testing.T) {
	works := seededWorks(t)
	defaults := []entity.OperationWorkDefault{
		{WorkToken: "topstitch", Field: "topstitch_width_mm", Value: "2", UpdatedAt: time.Now()},
		{WorkToken: "buttonhole", Field: "cut_length_mm", Value: "18", UpdatedAt: time.Now()},
	}

	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetOperationWorkCatalog(mock.Anything).Return(works, nil)
	tc.EXPECT().GetOperationWorkDefaults(mock.Anything).Return(defaults, nil)
	tc.EXPECT().ListOperationWorkSmvSamples(mock.Anything).Return(nil, nil)

	s := &Server{repo: repo}
	resp, err := s.GetOperationWorkCatalog(context.Background(), &pb_admin.GetOperationWorkCatalogRequest{})
	if err != nil {
		t.Fatalf("каталог не отдался: %v", err)
	}

	if len(resp.Works) != len(works) {
		t.Fatalf("работ приехало %d, в сиде %d", len(resp.Works), len(works))
	}
	if len(works) != 53 {
		t.Fatalf("в сиде 0329 стало %d работ вместо 53 — цитата фазы устарела, обнови её осознанно", len(works))
	}

	got := map[string]*pb_admin.OperationWorkCatalogItem{}
	for _, w := range resp.Works {
		got[w.Token] = w
	}
	for _, want := range works {
		w, ok := got[want.Token]
		if !ok {
			t.Fatalf("работа %s не доехала", want.Token)
		}
		if w.Verb != want.Verb || w.Stage != want.Stage || w.Label != want.Label ||
			w.MachineMode != want.MachineMode || w.DefaultMachine != want.DefaultMachine.String ||
			int(w.Sort) != want.Sort {
			t.Fatalf("работа %s приехала искажённой: %+v против %+v", want.Token, w, want)
		}
		if len(w.Machines) != len(want.Machines) {
			t.Fatalf("работа %s: машинок %d, в каталоге %d — ответ «на чём» потерян",
				want.Token, len(w.Machines), len(want.Machines))
		}
		for i := range want.Machines {
			if w.Machines[i] != want.Machines[i] {
				t.Fatalf("работа %s: машинка %q вместо %q", want.Token, w.Machines[i], want.Machines[i])
			}
		}
		if len(w.Syn) != len(want.Syn) {
			t.Fatalf("работа %s: синонимов %d, в каталоге %d", want.Token, len(w.Syn), len(want.Syn))
		}
		var cyr, lat bool
		for _, s := range w.Syn {
			if hasCyrillicRune(s) {
				cyr = true
			} else {
				lat = true
			}
		}
		if !cyr || !lat {
			t.Fatalf("работа %s доехала без %s слова поиска (syn=%v) — технолог не найдёт её своим словом",
				want.Token, map[bool]string{true: "латинского", false: "русского"}[cyr], w.Syn)
		}
		if w.Retired {
			t.Fatalf("работа %s помечена снятой, хотя сид её не снимал", want.Token)
		}
	}

	if len(resp.Defaults) != len(defaults) {
		t.Fatalf("дефолтов приехало %d, ожидалось %d", len(resp.Defaults), len(defaults))
	}
	if resp.Defaults[0].WorkToken != "topstitch" || resp.Defaults[0].Field != "topstitch_width_mm" ||
		resp.Defaults[0].Value != "2" {
		t.Fatalf("дефолт приехал искажённым: %+v", resp.Defaults[0])
	}
	if len(resp.DefaultFields) != len(entity.OperationWorkDefaultFields) {
		t.Fatalf("реестр полей приехал длиной %d, в entity %d — клиент нарисует жест не там",
			len(resp.DefaultFields), len(entity.OperationWorkDefaultFields))
	}
	for _, f := range resp.DefaultFields {
		if ve := entity.ValidateOperationWorkDefaultField(f); ve != nil {
			t.Fatalf("реестр отдал поле %q, которое сам же отвергает: %s", f, ve.Error())
		}
	}
}

// TestOperationWorkCatalogFlagsRetired — снятая работа отдаётся, но помечена. Клиент читает по ней
// старые строки шага и не предлагает её в пикере; отсутствие строки лишило бы его имени работы.
func TestOperationWorkCatalogFlagsRetired(t *testing.T) {
	works := []entity.OperationWork{
		{Token: "gather_ease", Verb: "machine", Label: "Gather / ease", MachineMode: "fixed",
			RetiredAt: sql.NullTime{Time: time.Now(), Valid: true}},
		{Token: "topstitch", Verb: "machine", Label: "Topstitch", MachineMode: "ask"},
	}
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetOperationWorkCatalog(mock.Anything).Return(works, nil)
	tc.EXPECT().GetOperationWorkDefaults(mock.Anything).Return(nil, nil)
	tc.EXPECT().ListOperationWorkSmvSamples(mock.Anything).Return(nil, nil)

	s := &Server{repo: repo}
	resp, err := s.GetOperationWorkCatalog(context.Background(), &pb_admin.GetOperationWorkCatalogRequest{})
	if err != nil {
		t.Fatalf("каталог не отдался: %v", err)
	}
	if len(resp.Works) != 2 {
		t.Fatalf("снятая работа выпала из ответа: %d работ", len(resp.Works))
	}
	if !resp.Works[0].Retired || resp.Works[1].Retired {
		t.Fatalf("флаг снятия расставлен неверно: %v / %v", resp.Works[0].Retired, resp.Works[1].Retired)
	}
}

// TestOperationWorkSmvHintComesFromTheLatestStep — ЦИТАТА подсказки нормы времени.
//
// Три шага с одной работой на двух карточках: подсказка одна, и в ней норма СВЕЖЕГО шага вместе с
// именем его карточки. Без сведения к последнему клиент получил бы список из трёх чисел и решал
// бы, какое из них «в прошлый раз», сам.
func TestOperationWorkSmvHintComesFromTheLatestStep(t *testing.T) {
	old := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	samples := []entity.OperationWorkSmvSample{
		{WorkToken: "topstitch", SMV: decimal.RequireFromString("0.7"), CardName: "TEE", CardUpdatedAt: old, OperationID: 11},
		{WorkToken: "topstitch", SMV: decimal.RequireFromString("1.4"), CardName: "COAT", CardUpdatedAt: recent, OperationID: 2},
		{WorkToken: "buttonhole", SMV: decimal.RequireFromString("0.2"), CardName: "TEE", CardUpdatedAt: old, OperationID: 12},
	}

	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetOperationWorkCatalog(mock.Anything).Return(nil, nil)
	tc.EXPECT().GetOperationWorkDefaults(mock.Anything).Return(nil, nil)
	tc.EXPECT().ListOperationWorkSmvSamples(mock.Anything).Return(samples, nil)

	s := &Server{repo: repo}
	resp, err := s.GetOperationWorkCatalog(context.Background(), &pb_admin.GetOperationWorkCatalogRequest{})
	if err != nil {
		t.Fatalf("каталог не отдался: %v", err)
	}
	if len(resp.SmvHints) != 2 {
		t.Fatalf("подсказок %d, ожидалось 2 (по одной на работу): %+v", len(resp.SmvHints), resp.SmvHints)
	}
	byToken := map[string]*pb_admin.OperationWorkSmvHint{}
	for _, h := range resp.SmvHints {
		byToken[h.WorkToken] = h
	}
	h := byToken["topstitch"]
	if h == nil || h.LastSmv.GetValue() != "1.4" || h.CardName != "COAT" {
		t.Fatalf("подсказка отстрочки взята не с последнего шага: %+v", h)
	}
	if b := byToken["buttonhole"]; b == nil || b.LastSmv.GetValue() != "0.2" || b.CardName != "TEE" {
		t.Fatalf("подсказка петли: %+v", b)
	}
}

// workCatalogSnapshot публикует снимок каталога на время теста — правило «такой работы нет» у
// записи дефолта спрашивает его, а не базу.
func workCatalogSnapshot(t *testing.T) {
	t.Helper()
	if entity.OperationWorkCatalogSnapshot() != nil {
		t.Fatal("снимок каталога уже опубликован — тесты этого пакета делят один процесс")
	}
	entity.SetOperationWorkCatalog([]entity.OperationWork{
		{Token: "topstitch", Verb: "machine", Label: "Topstitch", MachineMode: entity.OperationWorkMachineModeAsk},
		{Token: "buttonhole", Verb: "machine", Label: "Buttonhole", MachineMode: entity.OperationWorkMachineModeFixed},
	})
	t.Cleanup(func() { entity.SetOperationWorkCatalog(nil) })
}

// TestRememberOperationWorkDefaultRefusesMachineSetting — ЦИТАТА ПРАВИЛА РЕЕСТРА НА ВХОДЕ RPC.
//
// Машинная настройка отвергается InvalidArgument ПО ИМЕНИ ПОЛЯ и НЕ ДОЕЗЖАЕТ до хранилища. Второе
// важнее первого: мок собран БЕЗ ожидания записи, поэтому дошедший вызов сам по себе валит тест —
// правило проверяется не только словами отказа.
func TestRememberOperationWorkDefaultRefusesMachineSetting(t *testing.T) {
	workCatalogSnapshot(t)
	for _, field := range []string{"stitches_per_cm", "press_temperature_c", "needle_size_nm", "machine_type"} {
		repo := mocks.NewMockRepository(t)
		s := &Server{repo: repo}
		_, err := s.RememberOperationWorkDefault(context.Background(), &pb_admin.RememberOperationWorkDefaultRequest{
			WorkToken: "topstitch", Field: field, Value: "4",
		})
		if err == nil {
			t.Fatalf("%s: машинная настройка запомнена как дефолт вида — два механизма на одном поле", field)
		}
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%s: код отказа %s, ожидался InvalidArgument", field, status.Code(err))
		}
		if !contains(err.Error(), field) {
			t.Fatalf("%s: отказ не назвал поле: %s", field, err)
		}
		if !contains(err.Error(), "0306") {
			t.Fatalf("%s: отказ не сослался на лестницу профилей оборудования: %s", field, err)
		}
	}
}

// TestRememberOperationWorkDefaultStoresAWorkProperty — вторая половина: законное поле доезжает до
// хранилища ровно тем, чем его прислали.
func TestRememberOperationWorkDefaultStoresAWorkProperty(t *testing.T) {
	workCatalogSnapshot(t)
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)

	var gotWork, gotField, gotValue string
	tc.EXPECT().SetOperationWorkDefault(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, w, f, v string) { gotWork, gotField, gotValue = w, f, v }).
		Return(nil)

	s := &Server{repo: repo}
	if _, err := s.RememberOperationWorkDefault(context.Background(), &pb_admin.RememberOperationWorkDefaultRequest{
		WorkToken: "topstitch", Field: "topstitch_width_mm", Value: " 2 ",
	}); err != nil {
		t.Fatalf("законный дефолт отвергнут: %v", err)
	}
	if gotWork != "topstitch" || gotField != "topstitch_width_mm" || gotValue != "2" {
		t.Fatalf("до хранилища доехало (%q,%q,%q)", gotWork, gotField, gotValue)
	}
}

// TestRememberOperationWorkDefaultClearAlsoPassesTheRegistry — снятие проходит тот же реестр.
// Иначе «забудь дефолт stitches_per_cm» отвечало бы успехом и уверяло человека, что такой дефолт
// где-то был.
func TestRememberOperationWorkDefaultClearAlsoPassesTheRegistry(t *testing.T) {
	workCatalogSnapshot(t)

	repo := mocks.NewMockRepository(t)
	s := &Server{repo: repo}
	if _, err := s.RememberOperationWorkDefault(context.Background(), &pb_admin.RememberOperationWorkDefaultRequest{
		WorkToken: "topstitch", Field: "stitches_per_cm", Clear: true,
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("снятие машинной настройки: %v", err)
	}

	repo2 := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo2.EXPECT().TechCards().Return(tc)
	tc.EXPECT().DeleteOperationWorkDefault(mock.Anything, "topstitch", "topstitch_width_mm").Return(nil)
	s2 := &Server{repo: repo2}
	if _, err := s2.RememberOperationWorkDefault(context.Background(), &pb_admin.RememberOperationWorkDefaultRequest{
		WorkToken: "topstitch", Field: "topstitch_width_mm", Clear: true,
	}); err != nil {
		t.Fatalf("законное снятие отвергнуто: %v", err)
	}
}

// TestRememberOperationWorkDefaultChecksTheWorkAndTheValue — работа обязана существовать, значение
// обязано пройти те же правила, что и на шаге.
func TestRememberOperationWorkDefaultChecksTheWorkAndTheValue(t *testing.T) {
	workCatalogSnapshot(t)
	repo := mocks.NewMockRepository(t)
	s := &Server{repo: repo}

	if _, err := s.RememberOperationWorkDefault(context.Background(), &pb_admin.RememberOperationWorkDefaultRequest{
		WorkToken: "no_such_work", Field: "topstitch_width_mm", Value: "2",
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("незнакомая работа: %v", err)
	}
	if _, err := s.RememberOperationWorkDefault(context.Background(), &pb_admin.RememberOperationWorkDefaultRequest{
		WorkToken: "topstitch", Field: "topstitch_width_mm", Value: "2.25",
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("значение с лишним знаком: %v", err)
	}
	if _, err := s.RememberOperationWorkDefault(context.Background(), &pb_admin.RememberOperationWorkDefaultRequest{
		WorkToken: "topstitch", Field: "topstitch_width_mm", Value: "",
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("пустое значение без clear: %v", err)
	}
}

// TestRememberOperationWorkDefaultWithoutCatalogRefusesLoudly — незагруженный каталог даёт
// FailedPrecondition «этот сервер каталога не знает», а не «такой работы нет»: диагноз, уводящий
// искать опечатку в токене, хуже молчания (тот же довод, что у разбора шага в dto).
func TestRememberOperationWorkDefaultWithoutCatalogRefusesLoudly(t *testing.T) {
	if entity.OperationWorkCatalogSnapshot() != nil {
		t.Fatal("снимок каталога опубликован — тест проверял бы не то")
	}
	repo := mocks.NewMockRepository(t)
	s := &Server{repo: repo}
	_, err := s.RememberOperationWorkDefault(context.Background(), &pb_admin.RememberOperationWorkDefaultRequest{
		WorkToken: "topstitch", Field: "topstitch_width_mm", Value: "2",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("код отказа %s, ожидался FailedPrecondition: %v", status.Code(err), err)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
