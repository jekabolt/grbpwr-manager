package design_test

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ═══ ПЕРЕЧИСЛЕНИЕ ЛЕГАСИ-СОСТОЯНИЙ БЭКФИЛЛА 0359 ══════════════════════════════════════════════
//
// ⚠ ЭТА ПРОБА НАШЛА ДЕФЕКТ, РАДИ КОТОРОГО И НАПИСАНА, И ОСТАЁТСЯ НАВСЕГДА. Первая редакция 0359
// классифицировала по правилу «ревизия ребёнка отличается от родительской → правка, совпала →
// разрез», а шапка оправдывала промахи словами «ровно там, где ревизии совпали СЛУЧАЙНО».
// Случайности не было: СЛОЙ ВСЕГДА РОЖДАЕТСЯ НА РЕВИЗИИ 1 (layer.go, оба INSERT пишут литерал 1),
// поэтому ВТОРАЯ правка подряд — самый естественный второй жест человека — давала ребёнку ту же
// единицу, что у родителя, и штамповалась «разрезом». Это ровно тот дефект, который J-1 просил
// закрыть: лента складывала чистую правку в колоду нарезки.
//
// ПОЧЕМУ ДОКАЗАТЕЛЬСТВО, А НЕ ЭВРИСТИКА. Два писателя дают ЖЁСТКИЕ факты:
//   * разрез КОПИРУЕТ layer_rev родителя (pictures.go);
//   * флэттен пишет layer.Rev, а слой рождается на 1 и только инкрементируется — значит у
//     флэттена layer_rev ВСЕГДА ≥ 1 и НИКОГДА не 0.
// Отсюда: если у РОДИТЕЛЯ layer_rev = 0, ответ ОДНОЗНАЧЕН в обе стороны — ребёнок с нулём обязан
// быть разрезом (флэттен принёс бы ≥ 1), ребёнок с ненулём обязан быть флэттеном (разрез
// скопировал бы ноль). Если же у родителя layer_rev ≥ 1, вывести НЕЛЬЗЯ НИЧЕГО, и честный ответ —
// молчание. Бэкфилл поэтому классифицирует ровно доказуемое подмножество.
//
// ⚠ НЕПРАВИЛЬНЫЙ ШТАМП НЕИСПРАВИМ: `WHERE derivation = ''` не даст повтору его переписать, а у
// более поздней миграции доказательств будет не больше. Поэтому цена ошибки здесь — навсегда, и
// «промолчать» строго дешевле, чем «угадать».

type backfillState struct {
	name string
	// parentRev/childRev — layer_rev родителя и ребёнка.
	parentRev int
	childRev  int
	// parentDerived — родитель САМ производный (то есть у него есть свой родитель).
	parentDerived bool
	orphan        bool
	root          bool
	want          string
	why           string
}

func TestDesignDBDerivationBackfillEnumeratesEveryLegacyState(t *testing.T) {
	rep, raw := probeRepository(t)
	_ = rep
	card := probeCard(t, raw)

	states := []backfillState{
		{name: "корень без родителя", root: true, want: entity.DesignDerivationNone,
			why: "ни от чего не произведён — глагола нет"},
		{name: "разрез НЕредактированного корня", parentRev: 0, childRev: 0,
			want: entity.DesignDerivationCrop,
			why:  "флэттен принёс бы ревизию ≥ 1, значит ноль у ребёнка это скопированный ноль"},
		{name: "первая правка НЕредактированного корня", parentRev: 0, childRev: 1,
			want: entity.DesignDerivationFlatten,
			why:  "разрез скопировал бы ноль, значит единица могла прийти только от слоя"},
		{name: "правка поверх правки", parentRev: 1, childRev: 1, parentDerived: true,
			want: entity.DesignDerivationNone,
			why:  "ДЕФЕКТ ПЕРВОЙ РЕДАКЦИИ — здесь штамповался crop; слой родился на 1, совпадение не случайно"},
		{name: "разрез правки", parentRev: 1, childRev: 1, parentDerived: true,
			want: entity.DesignDerivationNone,
			why:  "настоящий разрез, но отличить его от строки выше НЕЧЕМ — молчание честнее догадки"},
		{name: "правка поверх разреза правки", parentRev: 1, childRev: 1, parentDerived: true,
			want: entity.DesignDerivationNone, why: "родитель отредактирован — вывести нельзя ничего"},
		{name: "третье поколение", parentRev: 2, childRev: 2, parentDerived: true,
			want: entity.DesignDerivationNone, why: "родитель отредактирован — вывести нельзя ничего"},
		{name: "сирота: родителя нет", orphan: true, want: entity.DesignDerivationNone,
			why: "derived_from без FK (0340); эвристике не на что опереться"},
		{name: "разрез НЕредактированного разреза", parentRev: 0, childRev: 0, parentDerived: true,
			want: entity.DesignDerivationCrop,
			why:  "довод про ноль зависит от ревизии РОДИТЕЛЯ, а не от того, корень ли он"},
		{name: "правка корня, слой правился трижды", parentRev: 0, childRev: 3,
			want: entity.DesignDerivationFlatten, why: "разрез скопировал бы ноль"},
		{name: "правка корня, который сам флэттен с чистого листа", parentRev: 1, childRev: 1,
			root: false, want: entity.DesignDerivationNone,
			why: "родитель — КОРЕНЬ на ревизии 1 (рисунок с нуля); ребёнок с той же единицей неразличим"},
	}

	ids := make([]int, len(states))
	for i, s := range states {
		var parent any
		switch {
		case s.root:
			parent = nil
		case s.orphan:
			parent = 2147483647
		default:
			// Родитель заводится тут же, со своей ревизией и своим происхождением.
			var grand any
			if s.parentDerived {
				grand = mkLegacyPicture(t, raw, card, nil, 0)
			}
			parent = mkLegacyPicture(t, raw, card, grand, s.parentRev)
		}
		ids[i] = mkLegacyPicture(t, raw, card, parent, s.childRev)
	}

	// Сбросить глагол у всех — состояние ДО миграции, когда писателей ещё не было.
	_, err := raw.Exec(`UPDATE design_picture SET derivation = '' WHERE tech_card_id = ?`, card)
	require.NoError(t, err)

	runMigrationUp(t, raw, "0359_design_picture_derivation.sql")

	// ⚠ СЧИТАЮТСЯ ИСХОДЫ, А НЕ «УПАЛО ЛИ». Одно require.Equal на первом же расхождении скрыло бы,
	// СКОЛЬКО состояний классифицируются неверно и в какую сторону, — а именно направление
	// («лишний crop») и есть вред.
	fails := 0
	for i, s := range states {
		var got string
		require.NoError(t, raw.QueryRow(`SELECT derivation FROM design_picture WHERE id = ?`, ids[i]).
			Scan(&got))
		if got != s.want {
			fails++
			t.Errorf("состояние %d %q: получено %q, ожидалось %q — %s", i+1, s.name, got, s.want, s.why)
		}
	}
	t.Logf("ИСХОДОВ=%d ПРОВАЛОВ=%d", len(states), fails)
}

func mkLegacyPicture(t *testing.T, raw *sql.DB, card int, parent any, layerRev int) int {
	t.Helper()
	media := probeMedia(t, raw)
	res, err := raw.Exec(`INSERT INTO design_picture
		(tech_card_id, media_id, ordinal, kind, derived_from, derivation, source_class, layer_rev)
		VALUES (?, ?, 0, 'flat', ?, '', 'ai', ?)`, card, media, parent, layerRev)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return int(id)
}
