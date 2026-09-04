package admin

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
)

// ═══ ВХОД ПЛАТНОГО ПРОГОНА ОБЯЗАН БЫТЬ КАРТИНКОЙ ═══════════════════════════════════════════════
//
// ЧТО ЭТО ЗАКРЫВАЕТ, ЗАМЕРЕННО И ПО ШАГАМ. Медиа опознаётся всюду ниже ТОЛЬКО номером и адресом:
// ни строка media, ни снимок прогона не хранят content type, и ни один читатель формат не
// спрашивал. Значит путь был такой: загрузить .glb новой дверью (UploadContentModel) → получить
// свежий НИЧЕЙНЫЙ media id → запустить прогон рода `render` с
// `params.extra_input_media_ids: [<этот id>]` → refuseForeignMedia пропускает ничейное медиа
// НАМЕРЕННО (store/design/layer.go: только что загруженный файл ещё не принадлежит карточке) →
// прогон РЕЗЕРВИРУЕТ деньги → воркер отдаёт поставщику адрес .glb в слоте картинки. Тот же id,
// зарегистрированный как `kind: "render"`, встаёт на рендер-плиту и уезжает в список картинок 3D.
//
// ⚠ «ЭТО БЫЛО ДОСТИЖИМО И РАНЬШЕ» — НЕ ОПРАВДАНИЕ, И ЭТО ГЛАВНОЕ. До двери моделей .glb-номер
// существовал только на карточке, УЖЕ ОПЛАТИВШЕЙ прогон 3D. Дверь чеканит такой номер по
// требованию, на любой карточке, из любого файла. Предусловие исчезло, и «раньше тоже можно было»
// перестало быть про тот же риск: вопрос не «достижимо ли», а «расширил ли я».
//
// ГДЕ СТОИТ ОТКАЗ И ПОЧЕМУ ИМЕННО ТАМ: у ДВЕРИ, ДО `Design().StartRun`, который резервирует деньги
// дня той же транзакцией, что вставляет строку. Отказ после него — это занятый резерв и `failed`
// в оплаченной истории за просьбу, которую можно было отклонить бесплатно. Та же позиция и тот же
// довод, что у `no_fabric_render`, `library_full` и `designRefuseThreedWithoutFront`.

// designVendorReadableMediaTypes — ЧТО ПОСТАВЩИК МОЖЕТ ПРОЧИТАТЬ В СЛОТЕ КАРТИНКИ.
//
// ⚠ ПЕРЕЧИСЛЯЕТСЯ РАЗРЕШЁННОЕ, А НЕ ЗАПРЕЩЁННОЕ, И ЭТО ПРО БУДУЩЕЕ. Новый род файла, который
// bucket научится хранить, попадёт сюда НЕ автоматически: он будет отвергнут, пока кто-нибудь не
// напишет про него строку. Обратный список (перечислить .glb и видео) молча пропустил бы
// следующий не-растровый тип — а следующий тип добавляют ровно тогда, когда о денежной двери
// никто не думает. Проба держит разбиение полным (design_input_format_test.go).
//
// ⚠ SVG ЗДЕСЬ ЕСТЬ, И ЭТО ЗАМЕРЕННОЕ РЕШЕНИЕ, А НЕ НЕДОСМОТР. Прогон рода `vector` рождает кадр
// рода **flat** (entity.DesignPictureKindOfRun: `default: flat`), и его медиа — САМ .svg. На бете
// это картинки 16, 25 и 66 (прогоны 5, 10, 19). Флэт-кадр — это ровно то, для чего существует
// флэт-слот верстака, а designSelectBench отдаёт плиты флэт-верстака КАЖДОМУ рендеру. Значит
// правило «не растр — отказ» отказывало бы КАЖДОМУ рендеру на карточке, где человек поставил на
// верстак собственный векторный выход продукта, — то есть было бы сторожем, который рубит
// законный оплаченный прогон. Читает ли поставщик .svg по адресу, отсюда не проверить
// (openrouter.validateImageURL гейтит только схему), поэтому отказ здесь был бы вкусом, а не
// замером. Названо и оставлено открытым; закрывается одной строкой, если это когда-нибудь измерят.
var designVendorReadableMediaTypes = map[string]struct{}{
	"image/jpeg":    {},
	"image/png":     {},
	"image/webp":    {},
	"image/gif":     {},
	"image/svg+xml": {},
}

// designInputMediaRef — один вход прогона: номер, адрес и ОТКУДА он приехал.
//
// `Where` несёт не украшение сообщения, а единственную подсказку, по которой человек чинит свой
// запрос: «медиа 812 не картинка» без имени поля отправляет его искать по четырём разным экранам.
type designInputMediaRef struct {
	ID    int
	URL   string
	Where string
}

// designRunInputMediaRefs — КАЖДЫЙ media id, который этот прогон отдаст поставщику, из ВСЕХ пяти
// источников сразу, без повторов и в порядке появления.
//
// ⚠ ПОЧЕМУ ПЯТЬ, А НЕ ТРИ. Границы карточки (designRefuseForeignMedia) спрашивают ТОЛЬКО три
// списка параметров, и это правильно для ИХ вопроса: «чей это номер» имеет смысл лишь для номера,
// пришедшего с провода, — плита верстака и референс принадлежат карточке по построению. Здесь
// вопрос ДРУГОЙ («что это за файл»), и на него плита с референсом отвечают ровно так же плохо:
// .glb, зарегистрированный кадром рода `render` и поставленный на рендер-плиту, — это ИМЕННО тот
// путь в список картинок 3D. Два вопроса, два множества; складывать их в одну проверку значило бы
// расширить границу карточки на то, для чего она не писана.
//
// ⚠ ПАРАМЕТРЫ ИДУТ ПЕРВЫМИ, И ЭТО ЗАМЕРЕННАЯ ПРАВКА, А НЕ ВКУС. Сначала здесь шёл снимок — и
// первая же проба показала, чем это плохо: `extra_input_media_ids` ВКЛАДЫВАЕТСЯ в `inputs.refs`
// (designAssembleInputs записывает в снимок всё, что поедет), поэтому номер, названный человеком в
// поле, приезжал в отказ как «a reference of this card» — то есть человека посылали чинить
// карточку вместо собственного запроса. Дедупликация оставляет ПЕРВЫЙ источник, значит порядок и
// есть правило атрибуции: сначала то, что назвал ВЫЗЫВАЮЩИЙ и может убрать сам, потом то, что
// принадлежит карточке. Обратной ошибки этот порядок не делает: настоящий референс карточки в
// параметрах не лежит вовсе.
func designRunInputMediaRefs(params *pb_common.DesignRunParams, inputs *pb_common.DesignInputSnapshot) []designInputMediaRef {
	seen := map[int]struct{}{}
	out := make([]designInputMediaRef, 0, 16)
	add := func(id int, where string) {
		if id <= 0 {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, designInputMediaRef{ID: id, Where: where})
	}

	for _, id := range params.GetExtraInputMediaIds() {
		add(int(id), "params.extra_input_media_ids")
	}
	add(int(params.GetColour().GetFabricMediaId()), "params.colour.fabric_media_id")
	for i, f := range params.GetColour().GetFabrics() {
		add(int(f.GetMediaId()), "params.colour.fabrics."+strconv.Itoa(i)+".media_id")
	}
	for _, sl := range inputs.GetSlots() {
		// Именная пустая деталь приезжает записью БЕЗ медиа — это просьба «нарисуй», а не картинка;
		// add отсеивает её нулём.
		where := "the bench plate on " + sl.GetViewKey()
		if name := strings.TrimSpace(sl.GetDetailName()); name != "" {
			where = "the bench plate on the detail «" + name + "»"
		}
		add(int(sl.GetMediaId()), where)
	}
	for _, r := range inputs.GetRefs() {
		where := "a reference of this card"
		if role := strings.TrimSpace(r.GetRole()); role != "" {
			where = "the card reference «" + role + "»"
		}
		add(int(r.GetMediaId()), where)
	}
	return out
}

// designFirstNonPictureInput — ПЕРВЫЙ вход, который картинкой не является, вместе с тем, чем он
// оказался. ok = false значит «все входы, чей адрес удалось опознать, — картинки».
//
// ⚠ НЕОПОЗНАННЫЙ АДРЕС ПРОПУСКАЕТСЯ, И ЭТО РЕШЕНИЕ С НАЗВАННОЙ ЦЕНОЙ. bucket.ObjectMediaType
// отвечает по расширению, которое сам же bucket и пишет; про адрес, пришедший откуда-то ещё, или
// про легаси-строку он честно говорит «не знаю». Превратить «не знаю» в отказ значило бы рубить
// оплаченный прогон на данных, которых я не видел (прод читать нельзя), — а сторож, отказывающий
// законному прогону, хуже дыры. По построению неопознанных нет: bucket — единственный писатель
// строк media, и на бете все 195 строк опознаются (128 webp, 61 png, 3 svg, 2 glb, 1 mp4).
// «По построению» на данных, которых я не вижу, — не то, на что стоит тратить чужой прогон.
func designFirstNonPictureInput(refs []designInputMediaRef) (designInputMediaRef, string, bool) {
	for _, ref := range refs {
		ct, known := bucket.ObjectMediaType(ref.URL)
		if !known {
			continue
		}
		if _, readable := designVendorReadableMediaTypes[ct]; !readable {
			return ref, ct, true
		}
	}
	return designInputMediaRef{}, "", false
}

// designNonPictureRefusal — один текст на обе двери. Он называет ТРИ вещи, и каждая нужна своему
// читателю: какой номер (человеку — что убрать), чем он оказался (ему же — почему), и что ничего
// не зарезервировано и не списано (ему же — что это не стоило денег).
func designNonPictureRefusal(ref designInputMediaRef, contentType string) error {
	return designRefusal(codes.FailedPrecondition, "input_not_a_picture",
		fmt.Sprintf("media %d is %s, and a generation reads its inputs as PICTURES: this file would "+
			"be handed to the provider in an image slot, where it cannot be read — the call would be "+
			"paid for and would fail. It came in as %s. Remove it there, or replace it with a "+
			"photograph or a drawing. Nothing was reserved and nothing was charged",
			ref.ID, contentType, ref.Where),
		map[string]string{
			"media_id":     strconv.Itoa(ref.ID),
			"content_type": contentType,
			"where":        ref.Where,
		})
}

// designRefuseNonPictureInputs — сама дверь для картиночного прогона.
//
// ОДИН ЗАПРОС В МЕДИА НА ВЕСЬ ПРОГОН, и он же единственный, который эта проверка добавляет к
// горячему пути. Дешевле было бы взять адреса у уже загруженной полосы (плиты и референсы её
// несут), но `extra_input_media_ids` — произвольные номера с провода, за которыми в полосу идти
// некуда, и два источника адресов для одного вопроса разошлись бы на первом же из них.
//
// ⚠ ПРОПАВШАЯ СТРОКА МЕДИА — НЕ ОТКАЗ. Тот же довод, что у designBoardPictureURLs и buildJob: id,
// которого нет у нас, поставщик тоже не скачает, и это не вопрос ФОРМАТА. Ронять здесь прогон
// значило бы отвечать отказом про формат на удалённую картинку.
//
// ⚠ РЕРАН ПРОВЕРЯЕТСЯ ТОЖЕ, И ЭТО НАМЕРЕННО. Довод дословно тот же, которым
// designRefuseUnworkableRecolourCloth спрашивает с ДЕЙСТВУЮЩИХ параметров, а не с сообщения
// клиента: неработоспособная форма прогона остаётся неработоспособной и на повторе, а
// замороженный прогон, чей вход поставщик прочитать не может, обязан быть остановлен здесь —
// иначе новый бинарь повторит его и заплатит за отказ ещё раз.
func (s *Server) designRefuseNonPictureInputs(ctx context.Context, params *pb_common.DesignRunParams, inputs *pb_common.DesignInputSnapshot) error {
	refs := designRunInputMediaRefs(params, inputs)
	if len(refs) == 0 {
		return nil
	}
	ids := make([]int, 0, len(refs))
	for _, r := range refs {
		ids = append(ids, r.ID)
	}
	byID, err := s.repo.Media().GetMediaByIds(ctx, ids)
	if err != nil {
		return designError(ctx, "failed to read the run's input media before starting it", err, nil)
	}
	for i := range refs {
		if m, ok := byID[refs[i].ID]; ok {
			refs[i].URL = m.FullSizeMediaURL
		}
	}
	if ref, ct, bad := designFirstNonPictureInput(refs); bad {
		return designNonPictureRefusal(ref, ct)
	}
	return nil
}

// designBoardMediaRefs — та же проверка для ТЕКСТОВОГО прогона, чьи картинки приходят с доски.
//
// ⚠ ОТДЕЛЬНЫЙ СБОРЩИК, А НЕ ВТОРОЙ ВЫЗОВ ТОГО ЖЕ, ПОТОМУ ЧТО ИСТОЧНИК ДРУГОЙ И АДРЕСА УЖЕ НА
// РУКАХ. `DraftDesignIdea` разрешает доску в адреса ДО денег своим собственным запросом
// (designBoardPictureURLs) — просить медиа второй раз значило бы добавить запрос ради того, что
// уже лежит рядом. Политика при этом читается ОДНА (designFirstNonPictureInput), а это
// единственное, чему расходиться нельзя.
//
// Доска не приходит с провода — это строки tech_card_media карточки, — но вопрос «что это за
// файл» от этого не меняется: .glb, прицепленный к доске, уезжает в тот же слот картинки того же
// платного вызова.
func designBoardMediaRefs(ids []int, urls []string) []designInputMediaRef {
	n := len(ids)
	if len(urls) < n {
		n = len(urls)
	}
	out := make([]designInputMediaRef, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, designInputMediaRef{ID: ids[i], URL: urls[i], Where: "the moodboard of this card"})
	}
	return out
}

// designUnreadableStorableTypes — какие ХРАНИМЫЕ типы эта политика не пускает в слот картинки.
// Существует ради пробы, которая держит разбиение полным: всякий тип, который bucket объявил
// хранимым, обязан быть либо читаемым поставщиком, либо перечисленным здесь. Список строится ИЗ
// bucket.StorableMediaTypes(), а не переписывается, — иначе проба сертифицировала бы копию.
func designUnreadableStorableTypes() []string {
	out := make([]string, 0, 2)
	for _, ct := range bucket.StorableMediaTypes() {
		if _, ok := designVendorReadableMediaTypes[ct]; !ok {
			out = append(out, ct)
		}
	}
	sort.Strings(out)
	return out
}
