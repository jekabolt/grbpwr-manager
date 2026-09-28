package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestFiberLabelTranslationsUpsert — переводы волокон для составников (care labels): миграция
// fiber_label_translation + сид, CreateFiber с переводами, UpsertFiberLabelTranslations как ПОЛНАЯ
// замена набора (пустое имя/пропущенный язык = удалить), флаг animal_non_textile, и что всё это
// видно в GetDictionaryInfo — выборке за admin GetDictionary.
//
// Только против одноразового контейнера (CI=1 + MYSQL_*): TestMain этого пакета дропает таблицы.
func TestFiberLabelTranslationsUpsert(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	d := s.Dictionary()

	dictFiber := func(code string) entity.Fiber {
		t.Helper()
		di, err := s.Cache().GetDictionaryInfo(ctx)
		require.NoError(t, err)
		for _, f := range di.Fibers {
			if f.Code == code {
				return f
			}
		}
		t.Fatalf("fibre %s not in dictionary payload", code)
		return entity.Fiber{}
	}
	rowCount := func(code string) int {
		t.Helper()
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM fiber_label_translation WHERE fiber_code = ?`, code).Scan(&n))
		return n
	}
	norm := func(pairs ...string) map[string]string {
		t.Helper()
		in := make([]entity.FiberLabelTranslation, 0, len(pairs)/2)
		for i := 0; i+1 < len(pairs); i += 2 {
			in = append(in, entity.FiberLabelTranslation{LabelLang: pairs[i], Name: pairs[i+1]})
		}
		m, err := entity.NormalizeFiberLabelTranslations(in)
		require.NoError(t, err)
		return m
	}
	boolp := func(b bool) *bool { return &b }

	// Сид миграции: базовое волокно несёт все 10 языков, кожа помечена флагом.
	cot := dictFiber("COT")
	require.Len(t, cot.LabelTranslations, 10, "seeded COT has every label language")
	require.Equal(t, "POLYAMIDE", dictFiber("NYL").LabelTranslations["en"], "seed: NYL prints as polyamide")
	require.True(t, dictFiber("LEA").AnimalNonTextile, "seed: leather is animal non-textile")
	require.False(t, cot.AnimalNonTextile)

	code := fmt.Sprintf("Y%d", time.Now().UnixNano()%10000000)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), `DELETE FROM fiber WHERE code = ?`, code) })

	// CreateFiber принимает переводы в той же транзакции.
	f, _, err := d.CreateFiber(ctx, code, "Test Fibre", norm("en", "TESTFIBRE", "fr", "FIBRETEST"), 0)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"en": "TESTFIBRE", "fr": "FIBRETEST"}, f.LabelTranslations)
	require.Equal(t, map[string]string{"en": "TESTFIBRE", "fr": "FIBRETEST"}, dictFiber(code).LabelTranslations)

	// Upsert #1: полный набор из 10 языков + флаг.
	full := []string{"en", "E", "fr", "F", "de", "D", "it", "I", "es", "S", "pt", "P", "nl", "N", "pl", "PL1", "cn", "棉", "jp", "綿"}
	f1, rev1, err := d.UpsertFiberLabelTranslations(ctx, code, norm(full...), boolp(true), 0)
	require.NoError(t, err)
	require.Len(t, f1.LabelTranslations, 10)
	require.True(t, f1.AnimalNonTextile)
	got := dictFiber(code)
	require.Len(t, got.LabelTranslations, 10, "GetDictionary carries all ten")
	require.Equal(t, "PL1", got.LabelTranslations["pl"])
	require.True(t, got.AnimalNonTextile)
	require.Equal(t, 10, rowCount(code))

	// Upsert #2: pl пустой (= удалить), fr новое значение, флаг не прислан (= не трогать).
	second := append([]string{}, full...)
	second[3] = "F2"   // fr
	second[15] = "   " // pl
	_, rev2, err := d.UpsertFiberLabelTranslations(ctx, code, norm(second...), nil, 0)
	require.NoError(t, err)
	require.Greater(t, rev2, rev1, "fibre revision advances")
	got = dictFiber(code)
	_, hasPL := got.LabelTranslations["pl"]
	require.False(t, hasPL, "empty pl deleted its row")
	require.Equal(t, "F2", got.LabelTranslations["fr"], "fr replaced, not kept")
	require.Len(t, got.LabelTranslations, 9)
	require.Equal(t, 9, rowCount(code), "no stale or duplicate rows")
	require.True(t, got.AnimalNonTextile, "absent flag leaves it as it was")

	// Upsert #3: язык, не присланный вовсе, тоже исчезает (полная замена); флаг снимается.
	_, _, err = d.UpsertFiberLabelTranslations(ctx, code, norm("en", "ONLY"), boolp(false), 0)
	require.NoError(t, err)
	got = dictFiber(code)
	require.Equal(t, map[string]string{"en": "ONLY"}, got.LabelTranslations)
	require.False(t, got.AnimalNonTextile)

	// Устаревшая версия — отказ, и транзакция не оставила следов.
	_, _, err = d.UpsertFiberLabelTranslations(ctx, code, norm("en", "STALE", "fr", "STALE"), boolp(true), 999999)
	require.ErrorIs(t, err, entity.ErrDictionaryVersionConflict)
	got = dictFiber(code)
	require.Equal(t, map[string]string{"en": "ONLY"}, got.LabelTranslations, "rejected write left nothing")
	require.False(t, got.AnimalNonTextile)

	// Неизвестное волокно — not found; язык вне списка на уровне стора — field violation, не 3819.
	_, _, err = d.UpsertFiberLabelTranslations(ctx, "NOPE9999", norm("en", "X"), nil, 0)
	require.ErrorContains(t, err, "not found")
	_, _, err = d.UpsertFiberLabelTranslations(ctx, code, map[string]string{"ja": "綿"}, nil, 0)
	var ve *entity.ValidationError
	require.True(t, errors.As(err, &ve), "unknown label language is a field violation, got %v", err)
	require.Equal(t, map[string]string{"en": "ONLY"}, dictFiber(code).LabelTranslations)
}
