package aiprov

import (
	"context"
	"net/http/httptrace"
	"sync/atomic"
)

// ObserveWrite arms THE ONE OBSERVER OF THE «BOUGHT / NOT BOUGHT» BOUNDARY on ctx and returns the
// context to send the request with, plus a func that answers — once Do has returned — whether the
// LAST transport attempt wrote the whole request (body included) without error. That answer is
// CallError.Engaged for every failure raised on the way back: from that moment the provider has the
// request and may be billing it, and whatever breaks afterwards breaks at our expense.
//
// Moved verbatim from openrouter.postChatCompletion (B-11) so that every HTTP transport of the stack
// (oaichat now; anthropic, gemini, oaimages, runblob as they are ported — 02-PLAN A2) asks the SAME
// question the SAME way. A transport that rolls its own copy is a transport whose engaged flag
// drifts from the others, and the router's fallback decision is only as honest as that flag.
//
// ⚠ ГРАНИЦА НАЙДЕНА НА ПРОВОДЕ, А НЕ В ПРОЗЕ ОШИБКИ, И ЭТО НЕСУЩЕЕ. Разобрать `*url.Error` по словам
// («timeout», «connection reset», «context canceled») — ровно тот способ, которым такая починка
// гниёт: строки ошибок net/http не контракт, они меняются между релизами Go и между прокси, а промах
// в любую сторону — это либо выдуманные деньги, либо снова спрятанные. Поэтому спрашивается САМ
// ТРАНСПОРТ: httptrace.WroteRequest срабатывает ровно тогда, когда запрос (вместе с телом) ДОПИСАН в
// соединение, и несёт собственную ошибку записи. Оборванная запись (info.Err != nil) флага НЕ
// поднимает — недописанное тело поставщик не обрабатывает.
//
// atomic, а не голый bool, и это не перестраховка: при истёкшем сроке Do возвращается из ОДНОЙ
// горутины, пока пишущая горутина транспорта ещё жива, и гонка тут была бы настоящей.
//
// ⚠ И ФЛАГ ОПИСЫВАЕТ ПОСЛЕДНЮЮ ПОПЫТКУ, А НЕ ОБЪЕДИНЕНИЕ ВСЕХ. Без сброса на GetConn он был
// МОНОТОННЫМ ИЛИ по попыткам транспорта, и это выдумывало деньги двумя дорогами:
//
//  1. WroteRequest СРАБАТЫВАЕТ ДО ТОГО, КАК БАЙТЫ ПОКИНУЛИ ПРОЦЕСС. Request.write ставит хук
//     defer'ом на СВОЙ именованный возврат (net/http/request.go), а pc.bw.Flush() зовётся
//     ПОСЛЕ неё, уже в writeLoop. Запрос, целиком уместившийся в 4 KiB bufio.Writer, поднимает
//     флаг, ни разу не коснувшись сокета; если Flush затем падает, не записав НИ БАЙТА,
//     транспорт заворачивает это в nothingWrittenError и — тело у нас bytes.Reader, значит
//     GetBody != nil — ПРОЗРАЧНО ПОВТОРЯЕТ запрос. Флаг между попытками не сбрасывался.
//  2. ЛЮБАЯ ДРУГАЯ ПОВТОРНАЯ ПОПЫТКА ОСТАВЛЯЛА ЕГО ПОДНЯТЫМ НАВСЕГДА: GOAWAY выше
//     LastStreamID, REFUSED_STREAM, протухшее соединение из пула (http.Client без своего
//     Transport берёт DefaultTransport с 90-секундным пулом, так что два нажатия внутри
//     полутора минут переиспользуют соединение, которое поставщик мог уже закрыть).
//
// GetConn СРАБАТЫВАЕТ РОВНО ОДИН РАЗ НА ПОПЫТКУ ТРАНСПОРТА — первой строкой Transport.getConn,
// то есть до выбора соединения из пула и до дозвона, и http/2 зовёт его же из своего пула на
// каждом круге RoundTripOpt. Поэтому он и выбран точкой сброса.
//
// ⚠ РЕДИРЕКТ ТОЖЕ СБРАСЫВАЕТ, И ЭТО ВЕРНО, А НЕ ПОБОЧНО. Client.do проводит редирект через
// новый RoundTrip (трасса живёт в контексте и переезжает вместе с ним), значит флаг описывает
// ПОСЛЕДНИЙ запрос. Ответ 3xx — это ВОРОТА, а не счётчик, ровно как 401/402/404/429: дописанный
// запрос, на который шлюз ответил «иди туда», ничего не купил.
//
// ⚠ ЧЕГО ЗДЕСЬ НАМЕРЕННО НЕТ: РАЗБОРА ПРОЗЫ ОШИБКИ. Сброс спрашивает ТОТ ЖЕ транспорт, что и подъём.
func ObserveWrite(ctx context.Context) (context.Context, func() bool) {
	var wrote atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GetConn: func(string) { wrote.Store(false) },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				wrote.Store(true)
			}
		},
	})
	return ctx, wrote.Load
}
