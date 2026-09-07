package httpapi

import (
	"strings"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/protobuf/encoding/protojson"
)

// ═══ ПУСТОЙ СПИСОК ПРЕСЕТОВ ДОЕЗЖАЕТ ДО КЛИЕНТА КЛЮЧОМ, А НЕ ТИШИНОЙ (m11) ═════════════════════
//
// ЧТО ЗА ВОПРОС. Клиент читает `freeform_presets` ПРИСУТСТВИЕМ: пустой список значит «сервер про
// плейграунд знает и говорит, что сейчас нельзя» — ячейка на рельсе гасится; ОТСУТСТВИЕ поля
// значит «сервер старый» — и тогда рельс рисуется по-другому. Прямая проба хендлера
// (TestGetDesignBandALWAYS_ANSWERS_ABOUT_THE_PLAYGROUND) держит половину этого: она смотрит на
// Go-значение, где nil и пустой срез РАЗЛИЧИМЫ. На проводе — не обязательно.
//
// ⚠ И В ДВОИЧНОМ PROTO ЭТО ПРАВДА НЕ РАЗЛИЧИМО. proto3 `repeated` без обёртки не несёт признака
// присутствия вовсе: пустой список не пишется в байты, и бинарный клиент не отличит нового
// сервера, у которого пресетов нет, от старого, у которого нет самого поля. Это свойство формата,
// а не дефект кода, и обёртывать поле в сообщение ради него значило бы менять контракт ради
// транспорта, которым admin-клиент не пользуется.
//
// ПОЭТОМУ ПРОБА ИЗМЕРЯЕТ ТОТ ТРАНСПОРТ, КОТОРЫМ КЛИЕНТ ДЕЙСТВИТЕЛЬНО ХОДИТ, — JSON гейтвея. У
// него EmitUnpopulated: true, значит КЛЮЧ печатается всегда, а «сервер старый» выглядит как
// отсутствие ключа в теле. Различие, невыразимое в байтах, на нашем проводе выразимо — и держится
// оно ровно одним битом опций, поэтому взято из ПРОДОВОГО маршалера (newAdminJSONMarshaler), а не
// собрано копией.
//
// МУТАЦИЯ ВШИТА В НАБОР (последний подтест): тот же ответ через маршалер БЕЗ EmitUnpopulated —
// ключ исчезает целиком, и клиент читает новый сервер как старый.
func TestDesignBandFreeformPresetsRideTheWireAsAPresentEmptyList(t *testing.T) {
	marshal := func(t *testing.T, resp *pb_admin.GetDesignBandResponse) string {
		t.Helper()
		raw, err := newAdminJSONMarshaler().Marshal(resp)
		if err != nil {
			t.Fatalf("ответ полосы не сериализовался: %v", err)
		}
		return string(raw)
	}

	t.Run("пустой список приезжает ключом с пустым массивом", func(t *testing.T) {
		body := marshal(t, &pb_admin.GetDesignBandResponse{FreeformPresets: []string{}})
		if !strings.Contains(body, `"freeformPresets":[]`) {
			t.Fatalf("клиент читает ПРИСУТСТВИЕ поля: без ключа он решит, что сервер старый, и "+
				"нарисует рельс по-другому. Тело: %s", body)
		}
	})

	t.Run("и nil — тоже: на этом проводе оба значат «пресетов нет»", func(t *testing.T) {
		// ⚠ ЭТО НЕ ДУБЛЬ ПРЕДЫДУЩЕГО ПОДТЕСТА, А ЕГО ВТОРАЯ ПОЛОВИНА. Хендлер обязан отдавать
		// не-nil (проба у него своя), но если он когда-нибудь отдаст nil, клиент НЕ должен
		// прочитать это как «сервер старый»: ключ печатается и здесь.
		body := marshal(t, &pb_admin.GetDesignBandResponse{})
		if !strings.Contains(body, `"freeformPresets":[]`) {
			t.Fatalf("незаполненный repeated обязан печататься пустым массивом, а не пропадать: %s", body)
		}
	})

	t.Run("непустой список едет значениями, а не числом", func(t *testing.T) {
		body := marshal(t, &pb_admin.GetDesignBandResponse{
			FreeformPresets: []string{"free", "add_hardware"},
		})
		if !strings.Contains(body, `"freeformPresets":["free","add_hardware"]`) {
			t.Fatalf("пресеты едут именами и в порядке ответа: %s", body)
		}
	})

	t.Run("МУТАЦИЯ: без EmitUnpopulated ключ исчезает вовсе", func(t *testing.T) {
		lax := &runtime.JSONPb{MarshalOptions: protojson.MarshalOptions{}}
		raw, err := lax.Marshal(&pb_admin.GetDesignBandResponse{FreeformPresets: []string{}})
		if err != nil {
			t.Fatalf("контрольная сериализация не удалась: %v", err)
		}
		if strings.Contains(string(raw), "freeformPresets") {
			t.Fatalf("контроль недействителен: ключ обязан пропасть без EmitUnpopulated, иначе "+
				"проба выше держится не тем битом, о котором говорит. Тело: %s", raw)
		}
	})
}
