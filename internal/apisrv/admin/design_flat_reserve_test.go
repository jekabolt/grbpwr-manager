package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestFlatReservationCoversTheQualityTheWorkerAsksFor — ШОВ МЕЖДУ ДВЕРЬЮ И ВОРКЕРОМ, и он тут
// потому, что M-3 («генерить флеты в максимально доступном разрешении») подняла флэту дил, а дил —
// «the single largest multiplier on what a press costs».
//
// Дверь резервирует по ПОТОЛКУ таблицы множителей, воркер просит КОНКРЕТНОЕ слово. Пока потолок
// покрывает это слово, резерв выше факта и дневной бюджет считает честно. Разойтись они могут
// ровно одним способом: у провайдера появится положение дороже `high`, кто-то поставит его флэту
// умолчанием и не впишет в designImageQualityFactor — и резерв молча станет ниже списания, в ту
// сторону, которая перерасходует. Этот тест — единственное место, где две половины встречаются.
func TestFlatReservationCoversTheQualityTheWorkerAsksFor(t *testing.T) {
	q := designgen.DefaultConfig().QualityFor(entity.DesignRunKindFlat)
	require.Equal(t, designgen.ImageQualityMax, q, "умолчание флэта — верх дила")

	factor, known := designImageQualityFactor[q]
	require.Truef(t, known, "флэт просит %q, а таблица множителей такого положения не знает: резерв взят вслепую", q)
	require.Truef(t, factor.LessThanOrEqual(designImageQualityCeiling),
		"множитель %s положения %q выше потолка %s — резерв ниже факта", factor, q, designImageQualityCeiling)
	require.Truef(t,
		designPriceEstimate[entity.DesignRunKindFlat].GreaterThanOrEqual(designImageMediumUSD.Mul(factor)),
		"резерв флэта %s не покрывает %s×%s", designPriceEstimate[entity.DesignRunKindFlat], designImageMediumUSD, factor)
}
