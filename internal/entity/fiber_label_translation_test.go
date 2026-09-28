package entity

import (
	"errors"
	"strings"
	"testing"
)

// Нормализация переводов волокна для ленты: закрытый список языков, без дублей, пустое имя = удалить.
func TestNormalizeFiberLabelTranslations(t *testing.T) {
	violation := func(t *testing.T, err error, field, reason string) {
		t.Helper()
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("err = %v, want *ValidationError", err)
		}
		if ve.Field != field || ve.Reason != reason {
			t.Fatalf("violation = %s/%s, want %s/%s", ve.Field, ve.Reason, field, reason)
		}
	}

	t.Run("full set, trimmed, lang case folded, empty name dropped", func(t *testing.T) {
		got, err := NormalizeFiberLabelTranslations([]FiberLabelTranslation{
			{LabelLang: "en", Name: " POLYAMIDE "},
			{LabelLang: " JP ", Name: "ポリアミド"},
			{LabelLang: "pl", Name: "   "},
		})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(got) != 2 || got["en"] != "POLYAMIDE" || got["jp"] != "ポリアミド" {
			t.Fatalf("got %v", got)
		}
		if _, ok := got["pl"]; ok {
			t.Fatalf("empty pl name must be dropped (= delete), got %v", got)
		}
	})

	t.Run("every label language is accepted", func(t *testing.T) {
		in := make([]FiberLabelTranslation, 0, len(LabelLangs))
		for _, l := range LabelLangs {
			in = append(in, FiberLabelTranslation{LabelLang: l, Name: "X"})
		}
		got, err := NormalizeFiberLabelTranslations(in)
		if err != nil || len(got) != 10 {
			t.Fatalf("got %d, err %v", len(got), err)
		}
	})

	t.Run("storefront code ja is not a label language", func(t *testing.T) {
		_, err := NormalizeFiberLabelTranslations([]FiberLabelTranslation{
			{LabelLang: "en", Name: "COTTON"},
			{LabelLang: "ja", Name: "綿"},
		})
		violation(t, err, "translations[1].label_lang", "unknown_label_lang")
	})

	t.Run("empty language code", func(t *testing.T) {
		_, err := NormalizeFiberLabelTranslations([]FiberLabelTranslation{{LabelLang: "", Name: "X"}})
		violation(t, err, "translations[0].label_lang", "unknown_label_lang")
	})

	t.Run("duplicate language, even differing in case", func(t *testing.T) {
		_, err := NormalizeFiberLabelTranslations([]FiberLabelTranslation{
			{LabelLang: "fr", Name: "COTON"},
			{LabelLang: "de", Name: "BAUMWOLLE"},
			{LabelLang: "FR", Name: "COTONNADE"},
		})
		violation(t, err, "translations[2].label_lang", "duplicate_label_lang")
	})

	t.Run("duplicate language where one is empty is still a duplicate", func(t *testing.T) {
		_, err := NormalizeFiberLabelTranslations([]FiberLabelTranslation{
			{LabelLang: "pl", Name: ""},
			{LabelLang: "pl", Name: "BAWEŁNA"},
		})
		violation(t, err, "translations[1].label_lang", "duplicate_label_lang")
	})

	t.Run("name longer than 64 characters", func(t *testing.T) {
		ok := strings.Repeat("綿", 64) // 64 символа, 192 байта — проходит: предел в символах
		if _, err := NormalizeFiberLabelTranslations([]FiberLabelTranslation{{LabelLang: "cn", Name: ok}}); err != nil {
			t.Fatalf("64 runes must pass, err %v", err)
		}
		_, err := NormalizeFiberLabelTranslations([]FiberLabelTranslation{{LabelLang: "cn", Name: ok + "綿"}})
		violation(t, err, "translations[0].name", "label_name_too_long")
	})

	t.Run("IsLabelLang is case-sensitive and closed", func(t *testing.T) {
		for _, l := range []string{"en", "cn", "jp"} {
			if !IsLabelLang(l) {
				t.Errorf("IsLabelLang(%q) = false", l)
			}
		}
		for _, l := range []string{"EN", "ja", "zh", "ru", ""} {
			if IsLabelLang(l) {
				t.Errorf("IsLabelLang(%q) = true", l)
			}
		}
	})
}
