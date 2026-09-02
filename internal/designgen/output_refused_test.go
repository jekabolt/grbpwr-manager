package designgen

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ОТВЕРГНУТАЯ ВЫДАЧА — ЭТО ТЕРМИНАЛЬНЫЙ ПРОВАЛ, А НЕ ПОГОДА (N4).
//
// ЧТО БЫЛО СЛОМАНО. Сторож CompleteRun, отказывающий роду кадра, который не может нести колорвей
// задания, откатывал транзакцию и оставлял строку ОТКРЫТОЙ. В реальном воркере платный вызов к
// этому моменту УЖЕ записан как delivered, ошибка уходила в abandon, а abandon строку не
// проваливает: лизинг истекал, очередь выдавала то же задание снова — и детерминированный баг
// маршрутизации покупал один и тот же плохой ответ до потолка платных попыток, заканчиваясь
// безымянным `lease_expired`.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: убрать ветку errors.Is(entity.ErrDesignColorwayForbidden) из classify —
// вердикт станет ретраебельным по умолчанию, и деньги снова потекут по кругу.
func TestClassifyMakesAStoreRefusalTerminal(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"род не несёт колорвей задания", fmt.Errorf("%w: run 3 is for colourway 5, but output 0 came back as \"flat\"",
			entity.ErrDesignColorwayForbidden)},
		{"два выхода с одним ординалом", fmt.Errorf("%w: two outputs share ordinal 0",
			entity.ErrDesignInvalidArgument)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := classify(tc.err)
			require.False(t, v.Retryable,
				"повтор покупает ещё один платный вызов и тот же ответ")
			require.Equal(t, CodeOutputRefused, v.Code,
				"строка обязана называть причину, а не заканчиваться lease_expired")
			require.Equal(t, entity.DesignAttemptDelivered, v.State,
				"поставщик своё отдал и получил деньги — состояние попытки это и говорит")
		})
	}
}

// А «СТРОКА БОЛЬШЕ НЕ НАША» ПО-ПРЕЖНЕМУ НЕ ПРОВАЛИВАЕТСЯ. Положительный контроль к разделению:
// без него «терминально» доказывало бы только то, что терминально ВСЁ, и перехваченное задание
// затиралось бы провалом того, кто его потерял.
func TestResultRefusedDoesNotSwallowALostClaim(t *testing.T) {
	require.True(t, designResultRefused(fmt.Errorf("%w: x", entity.ErrDesignColorwayForbidden)))
	require.True(t, designResultRefused(fmt.Errorf("%w: x", entity.ErrDesignInvalidArgument)))
	require.False(t, designResultRefused(fmt.Errorf("%w: x", entity.ErrDesignClaimLost)),
		"потерянный захват обязан уходить в abandon: провалить его значит затереть чужой результат")
	require.False(t, designResultRefused(fmt.Errorf("%w: x", entity.ErrDesignRunTerminal)))
	require.False(t, designResultRefused(errors.New("dial tcp: connection reset")),
		"погода — не отказ выдачи")
}
