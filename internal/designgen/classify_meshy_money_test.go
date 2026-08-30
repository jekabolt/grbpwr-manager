package designgen

import (
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/meshy"
	"github.com/stretchr/testify/require"
)

// TestMeshyOutOfCreditIsNotWeather — 402 от Meshy обязан читаться как «на счету пусто», и это
// утверждение стоит здесь потому, что БЕЗ НЕГО ОНО НЕ СТОРОЖИЛОСЬ НИЧЕМ. Строку
// `errors.Is(err, meshy.ErrOutOfCredit)` в classify.go можно было удалить целиком, и весь пакет
// оставался зелёным на 120 исполненных исходах — проверено мутацией, а не предположено.
//
// ЧТО ЛОМАЕТСЯ БЕЗ ЭТОЙ СТРОКИ. Ошибка проваливается в ретраебельный дефолт, и очередь платит за
// одно и то же задание пять раз подряд на счёте, где денег нет. Каждая попытка — новый резерв в
// дневном потолке, то есть пустой счёт съедает дневной бюджет владельца, ничего не произведя.
//
// ПОЧЕМУ МАЛО ПРОВЕРИТЬ «терминально». У Meshy рядом живёт ведро 4xx → ErrBadRequest, которое тоже
// терминально. Провалившись туда, отказ был бы закрыт правильно и НАЗВАН НЕВЕРНО: «мы отправили
// что-то не то» вместо «кончились деньги». По этим двум сообщениям действуют разные люди — первое
// чинит инженер, второе оплачивает владелец, — поэтому проба утверждает ИМЕННО КОД, а не только
// нератраебельность.
func TestMeshyOutOfCreditIsNotWeather(t *testing.T) {
	// Обёртка повторяет живой путь: воркер добавляет к ошибке провайдера свой контекст, и
	// classify обязана дотянуться до сентинела сквозь неё.
	got := classify(fmt.Errorf("designgen: run 7: %w", meshy.ErrOutOfCredit))

	require.False(t, got.Retryable,
		"402 — не погода: повтор платит второй раз на счёте, где нечем платить")
	require.Equal(t, CodeOutOfCredit, got.Code,
		"отказ обязан назвать причину деньгами, а не нашей ошибкой запроса: по этим двум "+
			"сообщениям действуют разные люди")
	require.Equal(t, entity.DesignAttemptFailed, got.State)
}

// TestMeshyMoneyAndKeyFaultsAreDistinct — 402 и 401 у Meshy обязаны РАЗЛИЧАТЬСЯ. Оба терминальны,
// и потому одинаковы для очереди; но «ключ отвергнут» и «денег нет» требуют разных действий, а
// единственное место, где эта разница выживает до человека, — error_code прогона.
func TestMeshyMoneyAndKeyFaultsAreDistinct(t *testing.T) {
	money := classify(fmt.Errorf("run: %w", meshy.ErrOutOfCredit))
	key := classify(fmt.Errorf("run: %w", meshy.ErrUnauthorized))

	require.NotEqual(t, key.Code, money.Code,
		"пустой счёт и отвергнутый ключ схлопнулись в один код — владелец не узнает, что оплатить")
	require.Equal(t, CodeOutOfCredit, money.Code)
	require.Equal(t, CodeUnauthorized, key.Code)
}
