package entity

import (
	"reflect"
	"regexp"
	"sort"
	"testing"
)

// Словари иконных полей (0435) пинятся ЦЕЛИКОМ: токен может лежать в подписанной карточке и в её
// дайджесте, а у каждого — свой рисунок в листе иконок клиента. Переименование, удаление или
// переезд вида в другой класс — это правка контракта, и она обязана быть видна в этом тесте.

var iconTokenPattern = regexp.MustCompile(`^[a-z0-9_]+$`)

func TestSeamKindVocabularyPinned(t *testing.T) {
	want := map[string]TechCardSeamClass{
		"bound_tape": "bs_bound", "bound_open": "bs_bound", "bound_closed": "bs_bound", "bound_frame": "bs_bound",

		"seam_edge": "ss_plain", "seam_one_side": "ss_plain", "seam_open": "ss_plain", "open_topstitched": "ss_plain",
		"piping_lapped": "ss_plain", "piping_faced": "ss_plain", "piping_cord": "ss_plain",
		"hong_kong": "ss_plain", "overcast_plain": "ss_plain", "safety_516": "ss_plain", "taped": "ss_plain",

		"french": "ss_french",

		"lapped_raw": "ls_lapped", "lapped_one_closed": "ls_lapped", "topstitch_closed": "ls_lapped", "bonded": "ls_lapped",

		"lapped_closed_a": "ls_flat_felled", "lapped_closed_b": "ls_flat_felled", "felled": "ls_flat_felled", "locked": "ls_flat_felled",

		"faced_split": "ef_faced", "faced_kant": "ef_faced", "faced_bias_closed": "ef_faced", "faced_bias_double": "ef_faced",

		"moscow_hem": "ef_hem_turned", "hem_closed": "ef_hem_turned", "blind_hem": "ef_hem_turned", "casing": "ef_hem_turned", "cover_hem": "ef_hem_turned",

		"hem_raw": "ef_hem_raw", "hem_zigzag": "ef_hem_raw", "edge_overlock": "ef_hem_raw",

		"flatlock": "fs_flat",

		"tucks": "os_topstitch",
	}
	if len(SeamKindTokens) != 38 {
		t.Fatalf("видов шва %d, контракт — 38", len(SeamKindTokens))
	}
	if !reflect.DeepEqual(SeamKindClass, want) {
		t.Fatalf("SeamKindClass разошёлся с контрактом:\n%v", SeamKindClass)
	}
	seen := map[string]bool{}
	for _, k := range SeamKindTokens {
		if seen[k] {
			t.Errorf("вид %q повторён", k)
		}
		seen[k] = true
		cls, ok := SeamKindClass[k]
		if !ok {
			t.Errorf("у вида %q нет класса", k)
			continue
		}
		if !ValidSeamClasses[cls] {
			t.Errorf("вид %q отнесён к несуществующему классу %q", k, cls)
		}
		if len(k) > SeamKindTokenMaxLen || !iconTokenPattern.MatchString(k) {
			t.Errorf("вид %q не влезает в колонку или не токен", k)
		}
	}
	if len(ValidSeamKinds) != len(SeamKindTokens) || len(SeamKindClass) != len(SeamKindTokens) {
		t.Fatalf("список, множество и карта классов разошлись: %d / %d / %d",
			len(SeamKindTokens), len(ValidSeamKinds), len(SeamKindClass))
	}
}

func TestSeamKindBindingStyleCoversExactlyBoundKinds(t *testing.T) {
	var bound []string
	for k, cls := range SeamKindClass {
		if cls == SeamClassBound {
			bound = append(bound, k)
		}
	}
	var keys []string
	for k, style := range SeamKindBindingStyle {
		keys = append(keys, k)
		if style != "" && !ValidBindingStyles[style] {
			t.Errorf("вид %q требует несуществующее сложение %q", k, style)
		}
	}
	sort.Strings(bound)
	sort.Strings(keys)
	if !reflect.DeepEqual(bound, keys) {
		t.Fatalf("ключи SeamKindBindingStyle %v != виды класса bs_bound %v", keys, bound)
	}
	want := map[string]TechCardBindingStyle{
		"bound_tape": "raw", "bound_open": "single_fold", "bound_closed": "double_fold", "bound_frame": "",
	}
	if !reflect.DeepEqual(SeamKindBindingStyle, want) {
		t.Fatalf("SeamKindBindingStyle разошёлся с контрактом: %v", SeamKindBindingStyle)
	}
}

func TestWorkVariantsPinned(t *testing.T) {
	want := map[string][]string{
		"set_zip":          {"open_end", "two_way", "magnetic", "faced"},
		"snap_press_stud":  {"ring", "spring_s"},
		"eyelet_grommet":   {"small", "washer"},
		"attach_elastic":   {"casing", "exposed", "gripper", "picot", "buttonhole", "loop"},
		"fuse":             {"full", "partial", "edge_tape", "tack"},
		"attach_hook":      {"eye", "trouser_bar"},
		"attach_hook_loop": {"tape", "dot"},
		"enclose_magnet":   {"pocket", "hem"},
		"insert_cord":      {"round", "flat"},
		"pleat":            {"knife", "box"},
		"pocket_welt":      {"single", "double"},
		"attach_patch":     {"plain", "embroidered"},
	}
	if !reflect.DeepEqual(WorkVariants, want) {
		t.Fatalf("WorkVariants разошёлся с контрактом:\n%v", WorkVariants)
	}
	for work, variants := range WorkVariants {
		seen := map[string]bool{}
		for _, v := range variants {
			if seen[v] {
				t.Errorf("%s: вариант %q повторён", work, v)
			}
			seen[v] = true
			if len(v) > WorkVariantTokenMaxLen || !iconTokenPattern.MatchString(v) {
				t.Errorf("%s: вариант %q не влезает в колонку или не токен", work, v)
			}
			if !IsWorkVariant(work, v) {
				t.Errorf("IsWorkVariant(%q, %q) = false", work, v)
			}
		}
	}
	if IsWorkVariant("set_zip", "edge_tape") || IsWorkVariant("join_lockstitch", "open_end") || IsWorkVariant("", "") {
		t.Fatal("IsWorkVariant пропустил чужой вариант")
	}
}

func TestButtonHoleVocabulary(t *testing.T) {
	if !reflect.DeepEqual(ButtonHoleCounts, map[int32]bool{2: true, 4: true}) {
		t.Fatalf("ButtonHoleCounts = %v", ButtonHoleCounts)
	}
	want := map[TechCardButtonAttachPattern]bool{"cross_x": true, "square": true, "u_shape": true}
	if !reflect.DeepEqual(ButtonAttachPatternsNeedFourHoles, want) {
		t.Fatalf("ButtonAttachPatternsNeedFourHoles = %v", ButtonAttachPatternsNeedFourHoles)
	}
	for p := range ButtonAttachPatternsNeedFourHoles {
		if !ValidButtonAttachPatterns[p] {
			t.Errorf("рисунок %q не существует", p)
		}
	}
}
