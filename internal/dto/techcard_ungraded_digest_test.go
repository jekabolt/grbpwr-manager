package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// UNI (0302) дописан в проекцию CONSTRUCTION ХВОСТОМ И ТОЛЬКО КОГДА TRUE. Стабильность непомеченной
// карточки пинится соседним TestUnmarkedCardConstructionDigestIsStable — тот же фикстур, тот же
// записанный хеш: если бы элемент писался безусловно, он бы упал первым, и упал бы как «все
// утверждённые подписи устарели в момент выкатки». Здесь проверяется вторая половина утверждения —
// что поле вообще хешируется, иначе первый тест удовлетворялся бы полем, которого нет в подписи.
func TestMarkingUngradedMovesTheConstructionDigest(t *testing.T) {
	base := TechCardSectionDigests(cutSymmetryDigestFixture())[entity.SignoffConstruction]

	marked := cutSymmetryDigestFixture()
	marked.Pieces[0].Ungraded = true
	require.NotEqual(t, base, TechCardSectionDigests(marked)[entity.SignoffConstruction],
		"пометка UNI не сдвинула отпечаток: подпись покрывала бы одно число контуров, а цех получил бы другое")

	// Пер-деталь, а не «где-то на карточке»: хвост дописывается в строку своей детали, и две
	// карточки, отличающиеся тем, КАКАЯ деталь помечена, обязаны различаться.
	third := cutSymmetryDigestFixture()
	third.Pieces[2].Ungraded = true
	require.NotEqual(t,
		TechCardSectionDigests(marked)[entity.SignoffConstruction],
		TechCardSectionDigests(third)[entity.SignoffConstruction])
}

// ХВОСТ ДОЛЖЕН ОСТАВАТЬСЯ ОДНОЗНАЧНЫМ. В одну и ту же позицию дописываются два разных условных
// элемента — строка cut_symmetry и bool ungraded, — и работает это ровно потому, что в JSON они
// кодируются по-разному. Тест держит именно это свойство: четыре состояния пары обязаны дать четыре
// РАЗНЫХ отпечатка. Если однажды кто-то добавит третий условный элемент того же типа, сломается
// здесь, а не в цехе.
func TestUngradedAndCutSymmetryTailsDoNotCollide(t *testing.T) {
	mark := func(sym string, ungraded bool) string {
		tc := cutSymmetryDigestFixture()
		tc.Pieces[0].CutSymmetry = sql.NullString{String: sym, Valid: sym != ""}
		tc.Pieces[0].Ungraded = ungraded
		return TechCardSectionDigests(tc)[entity.SignoffConstruction]
	}
	seen := map[string]string{}
	for _, c := range []struct {
		name     string
		sym      string
		ungraded bool
	}{
		{"neither", "", false},
		{"cut symmetry only", "identical", false},
		{"ungraded only", "", true},
		{"both", "identical", true},
	} {
		d := mark(c.sym, c.ungraded)
		if prev, dup := seen[d]; dup {
			t.Fatalf("состояния %q и %q дают ОДИН отпечаток CONSTRUCTION: хвост перестал быть однозначным", prev, c.name)
		}
		seen[d] = c.name
	}
}
