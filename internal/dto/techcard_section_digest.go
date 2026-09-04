package dto

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

// TechCardSectionDigests fingerprints each sign-off section's content, so an approval can be told
// apart from a stale approval (see TechCardSignoff.signed_digest).
//
// THE INVARIANT THAT MAKES THIS WORK: the same function runs on the WRITE payload (to stamp a
// section as it is approved) and on the READ model (to report the current fingerprint). Both are an
// entity.TechCardInsert — entity.TechCard embeds it — so the two agree as long as the projection
// below reads only fields that survive the store round-trip unchanged.
//
// That is why every value goes through the normalising helpers: a NULL string and an empty string
// must hash the same, or a section would read as "changed" purely because the store wrote NULL where
// the client sent "".
//
// DELIBERATELY NOT COVERED: TechCardInsert.Colorways. It is on the struct but it is populated only on
// READ (a colourway is a product; the style write path never carries them), so folding it in would
// make every COLOUR digest differ between write and read and mark every colour sign-off permanently
// stale. What the COLOUR section does cover is the card-owned colour decision: which BOM fabric each
// cut-piece is made from, per colourway.
//
// THE ONE PLACE THE TWO SIDES DO NOT AGREE BY THEMSELVES — LINKED BOM LINES: the BOM read query
// resolves a linked line's identity (name / supplier / supplier_ref / composition / spec / unit) from
// the catalog material it links, while the write payload legitimately carries an empty string for those
// fields (the admin client sends no name for a linked line that never had one — see
// parseTechCardBomItems and bomNaturalKey). materialsProjection hashes exactly those fields, so a
// MATERIALS approval stamped from the raw payload could never match the fingerprint the next read
// reports — a permanent, un-clearable "changed since sign-off". The write side therefore stamps through
// TechCardSectionDigestsAsRead, which resolves the link the same way the query does; the read side
// needs no help, the store enriched it already.
func TechCardSectionDigests(tc *entity.TechCardInsert) map[entity.TechCardSignoffSection]string {
	if tc == nil {
		return nil
	}
	return map[entity.TechCardSignoffSection]string{
		entity.SignoffDesign:       digestOf(designProjection(tc)),
		entity.SignoffConstruction: digestOf(constructionProjection(tc)),
		entity.SignoffMaterials:    digestOf(materialsProjection(tc)),
		entity.SignoffColour:       digestOf(colourProjection(tc)),
		entity.SignoffLabels:       digestOf(labelsProjection(tc)),
		entity.SignoffPackaging:    digestOf(packagingProjection(tc)),
		entity.SignoffCosting:      digestOf(costingProjection(tc)),
	}
}

// TechCardSectionDigestsToPb emits the current fingerprints for the wire, in a stable section order
// so the list does not reshuffle between reads.
func TechCardSectionDigestsToPb(tc *entity.TechCardInsert) []*pb_common.TechCardSectionDigest {
	digests := TechCardSectionDigests(tc)
	if len(digests) == 0 {
		return nil
	}
	order := []entity.TechCardSignoffSection{
		entity.SignoffDesign, entity.SignoffConstruction, entity.SignoffMaterials,
		entity.SignoffColour, entity.SignoffLabels, entity.SignoffPackaging, entity.SignoffCosting,
	}
	out := make([]*pb_common.TechCardSectionDigest, 0, len(order))
	for _, sec := range order {
		d, ok := digests[sec]
		if !ok {
			continue
		}
		out = append(out, &pb_common.TechCardSectionDigest{
			Section: techCardSignoffSectionEntityToPb[sec],
			Digest:  d,
		})
	}
	return out
}

// StampTechCardSignoffDigests records, for each APPROVED section, the fingerprint of the content that
// was approved — and, crucially, does NOT move it afterwards.
//
// The subtle part is when to stamp. Re-stamping on every save would be worse than useless: a save
// that edits the BOM would hand the materials sign-off a fresh digest matching the NEW content, so
// the section would silently re-bless itself and the staleness check could never fire. That is the
// exact failure this whole mechanism exists to prevent.
//
// So the stamp is driven by the caller's intent, expressed by what it sends back:
//
//	empty  → "I am approving this now" (a new sign-off, or a re-approval after review). The server
//	         fingerprints the payload it is about to write — the only moment it can be certain the
//	         digest describes precisely the content the approver was looking at.
//	present→ "I am not re-approving, I am just saving." The admin layer treats this only as a carry
//	         request and replaces the digest and audit fields from the stored approved row. Create has
//	         no stored row and coerces every approval to fresh.
//
// A section that is not approved carries no digest at all: pending and rejected have nothing to be
// stale against.
//
// This runs at parse time, with no DB in reach, so what it stamps is the payload AS SENT. The RPC layer
// re-stamps exactly the sections stamped here from the read-model form of the same payload
// (TechCardSectionDigestsAsRead) before the write — see restampFreshSignoffDigests. Keep this function
// the one that decides WHICH sections get stamped; the re-stamp only corrects the VALUE.
func StampTechCardSignoffDigests(tc *entity.TechCardInsert) {
	if tc == nil || len(tc.Signoffs) == 0 {
		return
	}
	digests := TechCardSectionDigests(tc)
	for i := range tc.Signoffs {
		if tc.Signoffs[i].State != entity.SignoffStateApproved {
			tc.Signoffs[i].SignedDigest = sql.NullString{}
			continue
		}
		if tc.Signoffs[i].SignedDigest.Valid && tc.Signoffs[i].SignedDigest.String != "" {
			continue // carry request; the admin layer verifies it against storage before persistence
		}
		tc.Signoffs[i].SignedDigest = nullStringFromPb(digests[tc.Signoffs[i].Section])
	}
}

// BomMaterialIdentity is the identity a LINKED BOM line inherits from the catalog material it links:
// exactly the fields the BOM read query resolves THROUGH the link (internal/store/techcard/materials.go,
// enrichMaterials). unit_price / currency are deliberately absent — the read path does not resolve them
// (an agreed cost stays frozen on the line), and colour is the line's own.
type BomMaterialIdentity struct {
	Name        string
	Supplier    string
	SupplierRef string
	Composition string
	Spec        string
	Unit        string
}

// TechCardSectionDigestsAsRead fingerprints a WRITE payload as the card will READ BACK: linked BOM
// lines take their identity from the catalog, the way the read query resolves it. catalog maps
// material_id → identity and only needs the materials the payload's BOM actually links; a missing id
// (or a nil map) leaves the line exactly as sent, which is also what the read query's LEFT JOIN does
// for a broken link.
//
// This is what the write side must stamp an approval with. TechCardSectionDigests on the raw payload
// answers a different question — "what did the client send" — and for a linked line that is not what
// any later read reports.
func TechCardSectionDigestsAsRead(tc *entity.TechCardInsert, catalog map[int64]BomMaterialIdentity) map[entity.TechCardSignoffSection]string {
	return TechCardSectionDigests(withResolvedBomIdentity(tc, catalog))
}

// withResolvedBomIdentity returns a copy of the insert whose linked BOM lines carry the identity the
// read query resolves for them. The copy is shallow apart from BomItems — nothing else is touched, and
// the caller's payload is never mutated: the enriched form is for hashing only, the stored columns keep
// the client's values (the read path re-resolves them on every read, which is the point of a link).
func withResolvedBomIdentity(tc *entity.TechCardInsert, catalog map[int64]BomMaterialIdentity) *entity.TechCardInsert {
	if tc == nil || len(catalog) == 0 || len(tc.BomItems) == 0 {
		return tc
	}
	out := *tc
	out.BomItems = make([]entity.TechCardBomItem, len(tc.BomItems))
	copy(out.BomItems, tc.BomItems)
	for i := range out.BomItems {
		b := &out.BomItems[i]
		if !b.MaterialId.Valid || b.MaterialId.Int64 <= 0 {
			continue // a free-text line has nothing to resolve from and keeps its own values
		}
		m, ok := catalog[b.MaterialId.Int64]
		if !ok {
			continue // link to a material we could not load: the stored value stands, as in the LEFT JOIN
		}
		b.Name = resolvedThroughLink(m.Name, b.Name)
		b.Supplier = resolvedNullThroughLink(m.Supplier, b.Supplier)
		b.SupplierRef = resolvedNullThroughLink(m.SupplierRef, b.SupplierRef)
		b.Composition = resolvedNullThroughLink(m.Composition, b.Composition)
		b.Spec = resolvedNullThroughLink(m.Spec, b.Spec)
		b.Unit = resolvedNullThroughLink(m.Unit, b.Unit)
	}
	return &out
}

// resolvedThroughLink mirrors the read query's COALESCE(NULLIF(m.x, empty), bi.x) rule: the catalog
// value wins whenever it is non-empty, otherwise the line's own value stands.
func resolvedThroughLink(catalog, stored string) string {
	if catalog != "" {
		return catalog
	}
	return stored
}

func resolvedNullThroughLink(catalog string, stored sql.NullString) sql.NullString {
	if catalog != "" {
		return sql.NullString{String: catalog, Valid: true}
	}
	return stored
}

func digestOf(v any) string {
	// json.Marshal walks struct fields in declaration order and map keys in sorted order, so the
	// encoding is stable for a given Go value — which is the only property this needs.
	b, err := json.Marshal(v)
	if err != nil {
		// A projection is plain data; a marshal failure means a programming error, not a data one.
		// Returning an empty digest would read as "no digest recorded", so make it loud instead.
		return fmt.Sprintf("unmarshalable:%v", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// blankAsAbsent — ОДНО НАПИСАНИЕ У ОДНОГО СОДЕРЖАНИЯ: блок, в котором не заполнено НИЧЕГО, даёт
// ровно тот же отпечаток, что и блок, которого нет вовсе. Каноничным выбрано ОТСУТСТВИЕ (nil), тем
// же решением, что уже принято у парка оборудования — см. equipmentProfilesTail: присутствие
// обёртки в отпечаток не входит, только её содержание.
//
// ПОЧЕМУ ИМЕННО ОТСУТСТВИЕ, А НЕ ПУСТАЯ ОБЁРТКА. Во-первых, «нет блока» — единственное написание,
// которое умеет произвести КАЖДАЯ из сторон: чтение отдаёт nil, когда строки нет, а запись — когда
// клиент про блок не говорил; пустую же обёртку рождает только часть путей (стор заводит пустой
// Construction карточке, у которой есть профили оборудования, но нет строки конструкции; клиент
// шлёт пустой блок, если пользователь открыл и закрыл вкладку). Схлопывать надо в то написание,
// которое общее. Во-вторых, канон «пустая обёртка» сдвинул бы отпечаток каждой карточки без блока
// — то есть почти всей базы; канон «отсутствие» двигает только карточки с пустой обёрткой, которых
// сегодня нет ни одной (tech_card_packaging — 0 строк).
//
// ПУСТОТА ВЫВЕДЕНА, А НЕ ПЕРЕЧИСЛЕНА: она считается прогоном НУЛЕВОЙ структуры через ТУ ЖЕ
// функцию-строитель. Второй список полей («какие из них считаются пустыми») разъехался бы со
// строителем при первой же дописке — молча и с расплатой в виде разъехавшегося отпечатка. Здесь
// новое поле попадает в проверку само: пока оно пусто, строка нулевой структуры совпадает с
// настоящей, а как только заполнено — расходится, и блок перестаёт быть пустым.
//
// Проекция обязана нормализовать NULL и "" в одно значение (см. заголовок файла) — тогда обёртка
// из одних NULL и обёртка из одних пустых строк тоже сойдутся здесь в одно nil.
func blankAsAbsent[T any](v *T, row func(*T) []any) any {
	if v == nil {
		return nil
	}
	var zero T
	filled := row(v)
	if digestOf(filled) == digestOf(row(&zero)) {
		return nil
	}
	return filled
}

// digestList — то же правило для СПИСКА: «пусто» имеет два написания (nil-срез и срез нулевой
// длины), json.Marshal пишет их как `null` и `[]`, и без сведения одно и то же содержание давало бы
// два отпечатка. Каноничным выбрано опять же ОТСУТСТВИЕ — так список отдают стор (append на
// nil-срез) и оба нормализатора разбора (annotationPieceKeys, TechCardCallout.PartList).
//
// Правило стоит В ПРОЕКЦИИ, а не у каждого производителя списка: производителей много и они
// разъезжаются (parseTechCardDetails собирал media_ids через make(..., 0, n) и отдавал `[]` там,
// где чтение отдаёт `null`, — отпечаток DESIGN такой карточки не совпадал сам с собой, то есть
// подпись рождалась протухшей и не лечилась переутверждением). Проекция — единственное место, через
// которое проходят ОБЕ стороны.
func digestList[T any](v []T) []T {
	if len(v) == 0 {
		return nil
	}
	return v
}

// --- section projections -------------------------------------------------------------------
//
// Each returns a plain, fully-normalised value. Field names are short because they are hashed, never
// read; what matters is that the SET of fields is exactly the content a signer is signing off.

type digestMedia struct {
	MediaID int    `json:"m"`
	Kind    string `json:"k"`
	Caption string `json:"c"`
	Cat     string `json:"g"`
}

func designProjection(tc *entity.TechCardInsert) any {
	media := make([]digestMedia, 0, len(tc.Media))
	for _, m := range tc.Media {
		media = append(media, digestMedia{
			MediaID: m.MediaId, Kind: string(m.Kind),
			Caption: m.Caption.String, Cat: string(m.Category),
		})
	}
	callouts := make([]any, 0, len(tc.Callouts))
	for _, c := range tc.Callouts {
		row := []any{
			c.Number, c.Part.String, c.Description.String, c.Dimensions.String,
			c.MediaId.Int32, digestDecimal(c.PosX), digestDecimal(c.PosY),
		}
		// ХВОСТ, А НЕ СЛОТ — тот же довод, что у cut_symmetry детали и у фотографий шага.
		// json.Marshal кодирует []any ПОЗИЦИОННО, поэтому восьмой элемент, поставленный
		// безусловно, сдвинул бы отпечаток КАЖДОЙ карточки в базе и объявил бы каждую подписанную
		// секцию DESIGN протухшей в момент деплоя — до того, как кто-нибудь нарисовал первую дугу.
		// Хвост дописывается только у выноски, которая перестала быть простым пином: карточка, где
		// геометрию никто не рисовал, хэшируется байт в байт как до 0309.
		//
		// Входит ли геометрия в подписываемое? Да, без оговорок: DESIGN — подпись под тем, ЧТО
		// нарисовано на эскизе, а мерка «6 мм» и скобка над участком это указание цеху, а не
		// метаданные о нём.
		//
		// ЦВЕТ — НЕ ВХОДИТ, ровно как у выносок на снимке шага: он различает пересекающиеся
		// указания и смысла не несёт, а протухшая подпись за перекраску наказывала бы за наведение
		// порядка на листе. По той же причине он и не открывает хвост.
		//
		// ВТОРОЙ ХВОСТ — ПОВЕРХ ПЕРВОГО, а не вместо. Пунктир, штриховка и список деталей приехали
		// позже (0310), и вписать их внутрь уже существующего хвоста значило бы сдвинуть отпечаток
		// каждой карточки, где кто-то успел нарисовать мерку. Второй хвост открывается только тем,
		// что этих фактов ТРЕБУЕТ, и обязательно тянет за собой первый: иначе восьмой элемент
		// означал бы то геометрию, то стиль, и различать их было бы нечем.
		//
		// ПУНКТИР И ШТРИХОВКА ВХОДЯТ В ПОДПИСЬ, в отличие от цвета: сплошная и пунктир на чертеже
		// говорят разное («шов» против «построения»), а контур и заливка — «эта граница» против
		// «эта площадь». Цвет же только различает пересекающиеся указания.
		// СПИСОК ДЕТАЛЕЙ БЕРЁТСЯ НОРМАЛИЗОВАННЫМ, а не сырым. Стор не пишет колонку `parts`, пока
		// деталь одна (она уже в `part`), поэтому сырое поле на ЗАПИСИ содержит ["полочка"], а на
		// ЧТЕНИИ той же строки — nil. Отпечаток тогда не совпадает сам с собой: подпись DESIGN
		// рождается протухшей и не лечится переутверждением, потому что повторный штамп берёт то
		// же расхождение. Одно правило на все три места — `PartList`.
		parts := c.PartList()
		geom := calloutKindOrPin(c.Kind) != entity.AnnotationKindPin || len(c.Points) > 0
		style := c.Dashed || c.Filled || len(parts) > 1
		// ТРЕТИЙ ХВОСТ — НАКОНЕЧНИК (0362), и открывается он ТОЛЬКО ЗАДАННЫМ. Пустой наконечник
		// означает «по виду» (мерка с засечками, скобка со скобками), то есть ровно ту картинку,
		// которая рисовалась и до круга 18; открывать хвост под него значило бы сдвинуть отпечаток
		// каждой карточки, где кто-то нарисовал мерку, — в момент выката и без единой правки.
		//
		// ТЯНЕТ ЗА СОБОЙ ОБА ПРЕДЫДУЩИХ, даже пустых, по тому же правилу, что второй тянет первый:
		// иначе девятый элемент кортежа означал бы то стиль, то наконечник, и прочесть подпись, не
		// угадывая, стало бы нельзя.
		//
		// ВХОДИТ В ПОДПИСЬ, как пунктир, а не как цвет: засечка говорит «этот участок измерен»,
		// стрелка — «смотри сюда», точка — «вот здесь». Это разные указания цеху.
		caps := c.Caps != "" && calloutKindOrPin(c.Kind).HasCaps()
		if geom || style || caps {
			points := make([]any, 0, len(c.Points))
			for _, p := range c.Points {
				points = append(points, []any{p.X.String(), p.Y.String()})
			}
			row = append(row, []any{string(calloutKindOrPin(c.Kind)), points})
		}
		if style || caps {
			row = append(row, []any{c.Dashed, c.Filled, parts})
		}
		if caps {
			row = append(row, string(c.Caps))
		}
		callouts = append(callouts, row)
	}
	details := make([]any, 0, len(tc.Details))
	for _, d := range tc.Details {
		// digestList — не перестраховка: разбор с провода собирает media_ids через make(..., 0, n) и
		// отдаёт `[]` у аспекта без картинок, а чтение оставляет срез nil и отдаёт `null`. Отпечаток
		// DESIGN такой карточки не совпадал сам с собой — подпись рождалась протухшей.
		details = append(details, []any{d.Key.String, d.Text.String, digestList(d.MediaIds)})
	}
	return []any{tc.Concept.String, media, callouts, details}
}

// constructionRow — головной кортеж секции: то, что карточка объявляет умолчаниями пошива.
//
// FIXED ORDER, FOREVER. Reordering these rewrites every stored CONSTRUCTION digest and marks
// every approved section as edited-since-signing. This tuple CHANGED SHAPE in the operations
// break (0289) — the free-text defaults became typed ones — so approvals on cards that
// carried construction defaults are stale exactly once, by design.
//
// ПОЗИЦИИ 3 И 5 ЗАМОРОЖЕНЫ НАВСЕГДА. В них жили overlock_thread_count и pressing, снятые
// парком оборудования (0306): один счёт нитей на карточку мог описать только один оверлок, а
// карточка шьётся на нескольких. УДАЛИТЬ элемент из позиционного массива — ровно тот же
// безусловный сдвиг, что и дописать: всё, что стоит правее, съезжает, и КАЖДАЯ карточка в
// базе, у которой обоих полей не было (то есть подавляющее большинство), получила бы новый
// отпечаток и объявила бы свою подпись CONSTRUCTION устаревшей в момент выкатки, ни за что.
// Поэтому на их местах стоят ровно те значения, в которые маршалился NULL: int32(0) и "".
// Прецедент — позиции 2-3 в costingProjection (бывшие hardware/packaging cost).
//
// Карточка, у которой эти два поля БЫЛИ заполнены, отпечаток меняет — и это честно, а не
// побочный ущерб: миграция 0306 перенесла её pressing в notes (позиция 6 ниже) и завела ей
// профиль оверлока, то есть подписанное содержание действительно двинулось. Волна
// переутверждения равна множеству таких карточек, а не всей базе.
func constructionRow(c *entity.TechCardConstruction) []any {
	return []any{
		c.DefaultSeamClass.String, digestDecimal(c.DefaultStitchesPerCm),
		int32(0), // была OverlockThreadCount; NULL маршалился как 0 — ЗАМОРОЖЕНО НАВСЕГДА
		c.HemFinish.String,
		"", // была Pressing; NULL маршалился как "" — ЗАМОРОЖЕНО НАВСЕГДА
		c.Notes.String,
	}
}

func constructionProjection(tc *entity.TechCardInsert) any {
	// ПУСТАЯ ОБЁРТКА = ЕЁ ОТСУТСТВИЕ (см. blankAsAbsent). Здесь это не гипотетика: стор САМ заводит
	// карточке пустой Construction, когда у неё есть профили оборудования, но нет строки конструкции
	// (0306, production.go), а запись такой карточки блока не несёт вовсе. Без сведения одно и то же
	// содержание — «умолчаний пошива нет» — давало бы на чтении и на записи разные отпечатки, то есть
	// вечное «изменено после подписи». Парк оборудования при этом остаётся: он висит на КАРТОЧКЕ, а
	// не на этой строке, и уезжает отдельным хвостом внешнего кортежа ниже.
	construction := blankAsAbsent(tc.Construction, constructionRow)
	// The operation tuple, also FIXED FOREVER. Unlike construction above, changing this one was
	// free: an EMPTY operations list marshals identically whatever shape the tuple has, and no card
	// on prod had a single operation when the break landed.
	//
	// Позиция 2 с 0306 — КОМПАТ-ПРОЕКЦИЯ, а не сырой тип шага: см. digestOperationTypeCompat.
	ops := make([]any, 0, len(tc.Operations))
	for i := range tc.Operations {
		o := &tc.Operations[i]
		row := []any{
			o.OperationNumber.Int32, digestOperationTypeCompat(o), string(o.Zone),
			// Позиции 4 и 5 — через digestList: пустой список обязан иметь ОДНО написание. Оба
			// сегодняшних производителя отдают nil, и голден-эталон шага фиксирует именно nil; правило
			// стоит здесь, чтобы третий производитель не завёл молча второе написание.
			digestList(o.PieceLineKeys), digestList(o.BomLineKeys),
			digestDecimal(o.SMV), o.CalloutNumber.Int32,
			digestDecimal(o.StitchesPerCm), o.SeamClass.String, digestDecimal(o.SeamAllowanceMm),
			o.TopstitchMode.String, digestDecimal(o.TopstitchWidthMm), o.TopstitchRows.Int32,
			o.AttachmentKind.String, digestDecimal(o.AttachmentSizeMm), o.Note.String,
		}
		// ДВА УСЛОВНЫХ ХВОСТА — МАШИНКА И ВТО, оба ПАРАМИ «имя колонки, значение», как и все
		// остальные. Доводы те же, что эта функция уже держит на деталях. (1) Безусловные элементы
		// сдвинули бы отпечаток КАЖДОГО шага в базе и объявили бы все утверждённые подписи
		// CONSTRUCTION устаревшими в момент выкатки; при условном появлении волна равна множеству
		// шагов, где факты реально заполнили. (2) Голый хвост запрещён уроком 0302/0304 в
		// комментарии к деталям: типы хвостов пересекаются (обе врезки здесь — массивы), и
		// ["press", …] против [ …] стало бы неразличимо; тег отвечает на вопрос, ЧЕЙ хвост, навсегда.
		//
		// ФОРМА ВЫРОВНЕНА, И ЭТО НЕ КОСМЕТИКА. С 0306 оба хвоста были ПОЗИЦИОННЫМИ, а 0325 добавил
		// ВТО ещё один хвост — парный — ПОД ТЕМ ЖЕ ТЕГОМ "press", потому что дописать поле в
		// позиционный массив нельзя. Тег перестал быть ключом: под "press" жили ДВА хвоста, и
		// различить их можно было только по форме. Теперь у одного тега ровно один хвост одной
		// формы, а дописка поля в него безопасна по построению — у шага, где новое поле пусто, пары
		// не возникает и байты не двигаются. Почему переписать форму законно ИМЕННО СЕЙЧАС и почему
		// это не прецедент — см. комментарий у opGoldConstructionDigestHex.
		//
		// В подписанном содержании этим фактам место без оговорок: CONSTRUCTION — подпись под тем,
		// ЧТО шьют и КАК. «Этот шов идёт на оверлоке в пять нитей иглой Nm 90» и «дублировать при
		// 150 °C двенадцать секунд» — указания цеху, меняющие физическое изделие, а не метаданные о
		// нём (в отличие от purpose/kind, исключённых намеренно — см. materialsProjection).
		if t := operationKindTail("machine",
			opKindStr("machine_type", machineTypeForTail(o)),
			opKindStr("machine_profile_key", o.MachineProfileKey),
			opKindInt("thread_count", o.ThreadCount),
			opKindStr("needle_type", o.NeedleType),
			opKindInt("needle_size_nm", o.NeedleSizeNm),
			opKindStr("thread_tension", o.ThreadTension),
			opKindStr("thread_tension_note", o.ThreadTensionNote),
			opKindDec("stitch_width_mm", o.StitchWidthMm),
		); t != nil {
			row = append(row, t)
		}
		// ПАР — ПАРОЙ ПО ПОСТРОЕНИЮ ФОРМЫ, и обёртка {Valid, Bool} больше не нужна: он трёхзначен
		// (не задано = наследовать профиль, явное «без пара» = указание цеху, «с паром»), а пара
		// рождается ТОЛЬКО у заполненного поля — значит отсутствие пары и есть «никто не отвечал».
		// Схлопывание «не задано» с «без пара» дало бы двум РАЗНЫМ режимам ВТО один отпечаток;
		// здесь оно невозможно.
		if t := operationKindTail("press",
			opKindStr("press_equipment", o.PressEquipment),
			opKindStr("press_profile_key", o.PressProfileKey),
			opKindInt("press_temperature_c", o.PressTemperatureC),
			opKindInt("press_dwell_sec", o.PressDwellSec),
			opKindDec("press_pressure_n_cm2", o.PressPressureNCm2),
			opKindBool("press_steam", o.PressSteam),
			opKindStr("press_cloth", o.PressCloth),
			opKindStr("press_action", o.PressAction),
			opKindStr("press_toward", o.PressToward),
		); t != nil {
			row = append(row, t)
		}
		// ТРЕТИЙ условный хвост — сборка (0307), по тем же двум доводам, что два выше.
		//
		// Хвост несёт ВЕСЬ упорядоченный union тегами «piece»/«unit», а не только узловые входы.
		// Иначе подпись получилась бы непоследовательной: перестановка двух ДЕТАЛЕЙ меняла бы
		// отпечаток (позиция 4 — упорядоченный список), а перестановка детали и узла — нет.
		// Старым подписям это ничем не грозит: у них хвоста нет вовсе.
		//
		// ИМЯ УЗЛА В ХВОСТ НЕ ВХОДИТ. Оно разрешается по первому производителю и фактом цеха не
		// является; хешировать его значило бы протухать подпись от невидимой правки на поглощающем
		// шаге. Ключ входит: он идентичность узла, и его смена — содержательная правка.
		if operationHasAssemblyTail(o) {
			row = append(row, []any{"assembly", o.OutputUnitKey.String, assemblyInputTail(o)})
		}
		// ФОТОГРАФИИ ШАГА С ВЫНОСКАМИ (0308) — ТОЖЕ ХВОСТОМ, И ТОЖЕ ТОЛЬКО ПРИ ЗАПОЛНЕННОСТИ.
		//
		// Довод дословно тот же, что у двух хвостов выше: json.Marshal кодирует []any позиционно,
		// и безусловный элемент сдвинул бы отпечаток КАЖДОЙ карточки, объявив все подписанные
		// CONSTRUCTION устаревшими в момент выката — до того, как кто-либо приложил хоть одну
		// фотографию. С хвостом волна пере-подписаний равна размеру кампании разметки.
		//
		// А ВХОДИТ ЛИ ЭТО В ПОДПИСАННОЕ СОДЕРЖИМОЕ ВООБЩЕ? Да. CONSTRUCTION — подпись под тем, ЧТО
		// и КАК шьют; указание «здесь припосадить 6 мм», нарисованное на снимке узла, — инструкция
		// цеху ровно того же рода, что припуск или класс шва. Сдвинули точку мерки — сменилось
		// указание, и подпись обязана протухнуть.
		//
		// Порядок картинок ВХОДИТ (это порядок показа и печати), подпись к картинке — тоже
		// (её читают на листе). Цвет — НЕТ: он различает пересекающиеся выноски и смысла не
		// несёт, а протухать подпись от перекраски значило бы наказывать за наведение порядка.
		if len(o.Media) > 0 {
			row = append(row, []any{"media", operationMediaTail(o)})
		}
		// ОДИННАДЦАТЬ ХВОСТОВ ВИДОВ ОПЕРАЦИЙ (0324) — ПОСЛЕ media, в замороженном порядке и ПАРАМИ
		// «ключ, значение» вместо позиций: форма, её цена и что именно в ней заморожено — в
		// комментарии у operationKindTails.
		//
		// НУЛЕВАЯ ВОЛНА: у каждой сегодняшней строки все 32 колонки NULL, значит ни одной пары, ни
		// одного хвоста, и байты кортежа не двигаются — при выкатке не протухает ни одна подпись.
		row = append(row, operationKindTails(o)...)
		// КОЛИЧЕСТВА НА СВЯЗЯХ ШАГА (0334) — ПАРНЫМ ХВОСТОМ, И САМЫМ ПОСЛЕДНИМ.
		//
		// ПОЗИЦИЯ 5 КОРТЕЖА (digestList(o.BomLineKeys)) ЗАМОРОЖЕНА КАК ЕСТЬ. Превратить голый список
		// ключей в пары «ключ, число» значило бы сдвинуть отпечаток КАЖДОГО шага, у которого есть
		// хоть один привязанный материал, и объявить все подписанные CONSTRUCTION устаревшими в
		// момент выкатки — до того, как кто-либо проставил хоть одно количество. Хвостом волна
		// пере-подписаний равна ровно множеству шагов, где число реально назвали; у шага без чисел
		// хвоста нет вовсе, и байты кортежа не двигаются ни на один.
		//
		// В ПОДПИСЬ ЭТО КЛАСТЬ НАДО. «Шаг ставит сюда 6 пуговиц» — инструкция цеху ровно той же
		// природы, что placement_count, который в хвосте уже хешируется: она меняет физическое
		// изделие, а не метаданные о нём. Проставить количество на утверждённой карточке и не
		// сдвинуть подпись значило бы подписать одно, а отдать в цех другое.
		//
		// ДЕТЕРМИНИРОВАННОСТЬ ПОРЯДКА — СВОЙСТВО ПОМОЩНИКА, А НЕ ВТОРАЯ СОРТИРОВКА ЗДЕСЬ.
		// operationKindTail сортирует пары ПОБАЙТНО по ключу (стабильно), поэтому два количества,
		// присланные клиентом в разном порядке, дают ОДИН отпечаток. Своя сортировка рядом была бы
		// вторым списком того же правила и разъехалась бы с первым при первой же правке.
		if t := operationKindTail("bom_qty", operationBomQtyPairs(o)...); t != nil {
			row = append(row, t)
		}
		ops = append(ops, row)
	}
	pieces := make([]any, 0, len(tc.Pieces))
	for _, p := range tc.Pieces {
		row := []any{
			p.LineKey, p.Name, p.PiecesPerGarment, p.Mirrored, p.Grainline,
			p.Fused, p.CalloutNumber.Int32, p.Note.String,
		}
		// A TAIL, NOT A SLOT — and this is not stylistics. json.Marshal encodes []any POSITIONALLY, so
		// a ninth element set UNCONDITIONALLY (be it "" or null) would shift the fingerprint of every
		// card in the database and declare EVERY approved CONSTRUCTION sign-off stale at the moment of
		// deploy — before anybody had marked anything. Appending only when the field is filled gives
		// exactly what is wanted: a card nobody has marked hashes byte for byte as it did before 0275,
		// and the only cards that go stale are the ones where a human actually ANSWERED the question —
		// including the answer «identical», because that too is an instruction to the floor, not the
		// absence of one. The re-approval wave is therefore the size of the marking campaign, not of
		// the rollout.
		//
		// Does the field belong in the signed content at all? Yes, without reservation. CONSTRUCTION is
		// the signature under WHAT is cut and sewn and HOW; "these two panels are a mirrored pair"
		// changes the physical part that comes out, not metadata about it (unlike purpose/is_sample,
		// which are excluded deliberately — see materialsProjection). Marking pairing on an approved
		// card without moving the signature would mean signing one thing and shipping another.
		//
		// p.Mirrored stays in slot 4 as a frozen false (0266 cleared every 1). REMOVING it is the same
		// unconditional shift as adding one: dropping an element breaks the fingerprint exactly as
		// appending one does. The column costs one BOOLEAN and one `false` in JSON; the rebase costs a
		// re-approval wave across every card at once. Do not tidy it away.
		if p.CutSymmetry.Valid {
			row = append(row, p.CutSymmetry.String)
		}
		// UNI (0302) — ХВОСТОМ И ТОЛЬКО КОГДА TRUE, по тому же доводу, что и сосед сверху: безусловный
		// элемент (пусть даже `false`) сдвинул бы отпечаток КАЖДОЙ карточки в базе и объявил бы все
		// утверждённые подписи CONSTRUCTION устаревшими в момент выкатки. Помеченных деталей сегодня
		// нет ни одной, значит волна переутверждения ровно равна кампании разметки.
		//
		// В подписанном содержании поле обязано быть: «этот карман один на весь ряд» — утверждение о
		// том, ЧТО кроят (сколько контуров существует и входит в каждый размер), а не метаданные о
		// нём. Утвердить карточку, потом пометить деталь UNI и не сдвинуть подпись — значит подписать
		// одно, а отдать в цех другое.
		//
		// ХВОСТ ОСТАЁТСЯ ОДНОЗНАЧНЫМ ТОЛЬКО ПОКА ТИПЫ НЕ ПЕРЕСЕКАЮТСЯ. Сейчас в него дописываются
		// строка (cut_symmetry) и bool (этот), поэтому любой набор состояний кодируется в JSON
		// по-своему и двух разных карточек с одинаковым отпечатком не бывает. СЛЕДУЮЩИЙ условный
		// элемент того же типа сломает это молча: [true] перестанет отвечать на вопрос, чей он.
		// Третьему полю нужен собственный различитель (пара «имя, значение»), а не ещё один голый
		// хвост.
		if p.Ungraded {
			row = append(row, p.Ungraded)
		}
		// РАЗМЕТКА ДУБЛИРОВАНИЯ (0304) — ТРЕТИЙ условный элемент, и он приходит ПАРОЙ «имя, значение»
		// ровно потому, что абзац выше это предписал. Голым хвостом его дописать нельзя: cut_symmetry
		// дописывается СТРОКОЙ, режим — тоже строка, и ["strip"] перестал бы отвечать на вопрос, чей
		// он. Это не гипотетика: карточка, где размечено дублирование и НЕ размечено «как кроится»,
		// столкнулась бы с карточкой, где наоборот, — и две разные конструкции получили бы один
		// отпечаток, то есть подпись под одной читалась бы как действительная под другой. Тег
		// закрывает это навсегда и оставляет место четвёртому полю.
		//
		// ТОЛЬКО КОГДА РАЗМЕЧЕНО — тот же довод, что у обоих соседей: безусловный элемент сдвинул бы
		// отпечаток КАЖДОЙ карточки в базе и объявил бы все утверждённые подписи CONSTRUCTION
		// устаревшими в момент выкатки, ни за что. Размеченных деталей сегодня нет ни одной, значит
		// волна переутверждения ровно равна кампании разметки.
		//
		// ШИРИНА ВХОДИТ В ПАРУ, а не хешируется отдельно: 10 мм и 25 мм — это разное количество
		// клеевой и разный физический край изделия, то есть разное содержание того же утверждения.
		// Пустая строка — не «ноль», а «своего числа нет», и она законно отличает «полосой 10 мм» от
		// «полосой по эталону припуска» (0328).
		//
		// В ПОДПИСАННОМ СОДЕРЖАНИИ ПОЛЕ ОБЯЗАНО БЫТЬ. CONSTRUCTION — подпись под тем, ЧТО кроят и
		// шьют; «эта деталь дублируется только по краю на 25 мм» описывает физическую деталь, которая
		// выйдет из цеха, а не метаданные о ней (в отличие от purpose/is_sample, исключённых
		// намеренно). Утвердить карточку, потом сменить дублирование целиком на полосу и не сдвинуть
		// подпись — значит подписать одно, а отдать в цех другое, причём с разницей в разы по клеевой.
		if p.FusingMode.Valid {
			row = append(row, []any{"fusing", p.FusingMode.String, digestDecimal(p.FusingWidthMm)})
		}
		pieces = append(pieces, row)
	}
	out := []any{construction, ops, pieces}
	// ПАРК ОБОРУДОВАНИЯ (0306) — УСЛОВНЫЙ ХВОСТ ВНЕШНЕГО КОРТЕЖА, тот же приём и те же два довода,
	// что у хвостов шага выше: безусловный четвёртый элемент сдвинул бы отпечаток КАЖДОЙ карточки в
	// базе, а тег «equipment» отвечает на вопрос, чей хвост, когда рядом встанет пятый.
	//
	// Профили входят в подпись, потому что шаг наследует от них ЖИВЬЁМ: строка шага хранит только
	// то, чем он отличается, а «на чём и как» читается из профиля в момент чтения. Сменить профилю
	// температуру и не сдвинуть подпись — значит подписать 150 °C, а отдать в цех 190.
	if tail := equipmentProfilesTail(tc.Construction); tail != nil {
		out = append(out, tail)
	}
	return out
}

// legacyOperationTypeOf says whether a step collapses into the ONE legacy operation-type token it
// used to be stored as, and which one. The pair (machine, <machine token>) is exactly what migration
// 0306 split the nine legacy types into, so the mapping back is total on those nine and empty on
// everything else.
func legacyOperationTypeOf(o *entity.TechCardOperation) (string, bool) {
	if o.OperationType != entity.OpTypeMachine || !o.MachineType.Valid {
		return "", false
	}
	legacy, ok := entity.MachineTypeLegacyToken[o.MachineType.String]
	return legacy, ok
}

// digestOperationTypeCompat hashes a step's type AS IT WAS STORED BEFORE 0306: (machine, lockstitch)
// hashes byte for byte as the string "lockstitch" did. This is what keeps migration day from
// declaring every signed CONSTRUCTION section stale — the rows 0306 rewrote did not change their
// CONTENT, only the two columns it is spread across, and a fingerprint must follow content.
//
// ОДНОЗНАЧНОСТЬ, а не удобство: девять legacy-токенов и семь токенов нового словаря
// (entity.OperationTypeTokens) НЕ ПЕРЕСЕКАЮТСЯ, поэтому "lockstitch" в этой позиции читается ровно
// одним способом. Биекция полная в обе стороны: entity.MachineTypeLegacyToken строится инверсией
// LegacyOperationMachineType и паникует при init на неинъективной правке, а DTO канонизирует legacy
// с провода ДО построения entity — значит шага с сырым legacy-типом после Ф1 не существует, и
// «мигрированный» и «нововведённый» варианты одного шага дают один отпечаток по построению.
//
// Машинка, которой в legacy не было (coverlock, zigzag, автоматы), не схлопывается: позиция несёт
// "machine", а сама машинка уезжает в machine-хвост. Такой карточки до 0306 существовать не могло,
// поэтому протухать нечему.
func digestOperationTypeCompat(o *entity.TechCardOperation) string {
	if legacy, ok := legacyOperationTypeOf(o); ok {
		return legacy
	}
	return string(o.OperationType)
}

// machineTypeForTail отдаёт машинку МАШИННОМУ ХВОСТУ и молчит, когда она уже уехала в
// компат-позицию: там она и хешируется, а пара "machine_type" здесь записала бы один факт дважды.
// Неоднозначности нет — компат-позиция при этом несёт legacy-токен, которого нет в новом словаре
// типов.
//
// Отсюда же и предикат «есть ли машинный хвост», которого больше нет отдельной функцией: у
// мигрированной строки `lockstitch → (machine, lockstitch)` машинка молчит, семь пере-определений
// шага пусты, набор пар пуст — и хвост не рождается, как не рождался до 0306. Предикат ВЫВЕДЕН из
// состава пар и потому не может с ним разъехаться; отдельный список тех же полей разъехался бы при
// первой же дописке. То же и у ВТО: «без пара» — это Valid, значит пара, значит хвост, значит
// отпечаток, отличный от шага, где никто не отвечал.
func machineTypeForTail(o *entity.TechCardOperation) sql.NullString {
	if _, collapsed := legacyOperationTypeOf(o); collapsed {
		return sql.NullString{}
	}
	return o.MachineType
}

// operationHasAssemblyTail — есть ли у шага сборочные факты (0307).
//
// Вход-ДЕТАЛЬ фактом сборки не является: он есть у каждой сегодняшней карточки и уже стоит
// позицией 4 базового кортежа. Хвост появляется только там, где шаг что-то производит или берёт
// со стола УЗЕЛ, — то есть сегодня не появляется нигде, и ни одна действующая подпись
// CONSTRUCTION при выкатке не протухает.
func operationHasAssemblyTail(o *entity.TechCardOperation) bool {
	if o.OutputUnitKey.Valid && o.OutputUnitKey.String != "" {
		return true
	}
	for _, in := range o.AssemblyInputs {
		if in.Kind == entity.AssemblyInputUnit {
			return true
		}
	}
	return false
}

// operationMediaTail проецирует фотографии шага: media_id, подпись и выноски по порядку.
//
// Цвет в проекцию не входит — см. довод у места вызова. Координаты берутся строкой decimal, а не
// float64: тот же тип, что на проводе и в БД, и никакого округления по дороге в отпечаток.
func operationMediaTail(o *entity.TechCardOperation) []any {
	out := make([]any, 0, len(o.Media))
	for _, m := range o.Media {
		anns := make([]any, 0, len(m.Annotations))
		for _, a := range m.Annotations {
			points := make([]any, 0, len(a.Points))
			for _, p := range a.Points {
				points = append(points, []any{p.X.String(), p.Y.String()})
			}
			ann := []any{
				string(a.Kind), points, a.Text, a.LabelX.String(), a.LabelY.String(),
			}
			// Деталь — ХВОСТОМ, по той же причине, что и всё прочее дописанное к подписываемому
			// кортежу: шестой элемент, поставленный безусловно, сдвинул бы отпечаток каждого
			// снимка, у которого выноски уже есть. Указание, называющее ДЕТАЛЬ, — часть
			// инструкции цеху («эту строчку на подборте»), поэтому в подпись входит.
			//
			// ВТОРОЙ ХВОСТ (0310: пунктир, штриховка, список деталей) кладётся ПОВЕРХ первого и
			// обязательно тянет его за собой — даже пустым. Иначе шестой элемент означал бы то
			// деталь, то стиль, и прочесть кортеж, не угадывая, стало бы нельзя. Указание с одной
			// деталью и без пунктира хешируется байт в байт как до 0310.
			keys := a.PieceLineKeys
			if len(keys) == 0 && a.PieceLineKey != "" {
				keys = []string{a.PieceLineKey}
			}
			first := ""
			if len(keys) > 0 {
				first = keys[0]
			}
			style := a.Dashed || a.Filled || len(keys) > 1
			// ТРЕТИЙ ХВОСТ (0362: наконечник линии) — тем же правилом, что и второй: открывается
			// только ЗАДАННЫМ наконечником и обязательно тянет за собой оба предыдущих. Пустой
			// наконечник — «по виду», то есть та же картинка, что и до круга 18, поэтому снимок
			// шага, где никто не выбирал концы, хешируется байт в байт как до 0362.
			caps := a.Caps != "" && a.Kind.HasCaps()
			if first != "" || style || caps {
				ann = append(ann, first)
			}
			if style || caps {
				ann = append(ann, []any{a.Dashed, a.Filled, digestList(keys)})
			}
			if caps {
				ann = append(ann, string(a.Caps))
			}
			anns = append(anns, ann)
		}
		out = append(out, []any{m.MediaId, m.Caption.String, anns})
	}
	return out
}

// ── ПАРНЫЕ ХВОСТЫ ШАГА (волна 0324, и с 0325-b — ВСЕ хвосты полей, включая machine и press) ─────
//
// ФОРМА — ПАРЫ, А НЕ ПОЗИЦИИ. Хвост семейства пишется как ["тег", [[ключ, значение], …]], пара
// появляется ТОЛЬКО у заполненного (Valid) поля, пары отсортированы ПОБАЙТНО по ключу. Это
// ЕДИНСТВЕННАЯ форма дописки к кортежу шага: голова остаётся позиционной, всё правее неё — пары.
// Исключений ровно два — assembly и media: они несут не поля, а СПИСКИ объектов (упорядоченный
// union входов; снимки с выносками), и пара «ключ, значение» такую вложенность не выражает.
//
// ЗАЧЕМ. Волна урезана вдвое: из 66 разобранных полей в неё вошли 32, а остальные 32 вернутся
// ПОЗЖЕ И В ТЕ ЖЕ САМЫЕ СЕМЕЙСТВА. При позиционной записи дописать поле в уже рождённый хвост
// нельзя ни в конец, ни в середину — длина и порядок массива входят в байты, и подпись каждой
// карточки, которая хвост эмитит, протухла бы. Позиционная схема заставила бы завести
// "stitching2", "hardware2", "print2", "trim2", "thread_trim2", "clean2", "inspect2",
// "fastening2" — восемь вечных костылей в самой чувствительной структуре проекта, и всё это ради
// экономии на именах ключей.
//
// С парами дописывание БЕЗОПАСНО ПО ПОСТРОЕНИЮ: у строки, где новое поле пусто, пары не
// возникает, и её байты не меняются; у строки, где оно заполнено, отпечаток обязан измениться —
// это новый факт, и подпись должна протухнуть. Ровно то поведение, которого мы хотим.
//
// ЗАМОРОЖЕНО НАВСЕГДА: имена тегов, порядок хвостов в кортеже, имена ключей и каноникализация
// значений. СОСТАВ — НЕ заморожен, в этом весь смысл.
//
// КЛЮЧ ПАРЫ — ИМЯ КОЛОНКИ БД, А НЕ ИМЯ ПОЛЯ PROTO. Расходятся ровно три места, и все три —
// намеренно: TechCardOperationPlacement.count → "placement_count", TechCardOperationTrim.action →
// "trim_action", TechCardOperationClean.kind → "cleaning_kind". Взять там имя proto значило бы
// заморозить навсегда ключи "count" / "action" / "kind": без семейства в имени они читаются как
// что угодно, а переименовать ключ после первой подписи уже нельзя — это ломает отпечаток.
//
// ОТДЕЛЬНОГО ПРЕДИКАТА «есть ли хвост» нет ни у одного парного хвоста — ни у одиннадцати
// семейств, ни у machine, ни у press. Он был бы вторым списком тех же полей и разъехался бы с
// первым при первой же дописке; здесь предикат ВЫВЕДЕН: пустой набор пар — значит хвоста нет.
// Отдельный предикат остался только у assembly, и по другому поводу: его хвост открывают не
// «любые непустые поля», а конкретный факт — шаг что-то производит или берёт со стола УЗЕЛ.

// operationKindPair — одна пара «ключ, значение» хвоста семейства.
type operationKindPair struct {
	key   string
	value any
	valid bool
}

func opKindStr(key string, v sql.NullString) operationKindPair {
	return operationKindPair{key: key, value: v.String, valid: v.Valid}
}

func opKindInt(key string, v sql.NullInt32) operationKindPair {
	return operationKindPair{key: key, value: v.Int32, valid: v.Valid}
}

// opKindDec — децимал СТРОКОЙ через digestDecimal, как всё остальное в этой проекции: float64 дал
// бы 1.50 и 1.5 разными байтами в зависимости от того, что вернула БД.
func opKindDec(key string, v decimal.NullDecimal) operationKindPair {
	return operationKindPair{key: key, value: digestDecimal(v), valid: v.Valid}
}

// opKindBool — трёхзначный bool БЕЗ обёртки {Valid, Bool}: третье состояние выражает сама форма
// пар. «Не задано» пары не даёт, явное «нет» даёт ["ключ", false], «да» — ["ключ", true], и три
// состояния дают три разных отпечатка. Обёртка здесь была бы не защитой, а лишними байтами.
func opKindBool(key string, v sql.NullBool) operationKindPair {
	return operationKindPair{key: key, value: v.Bool, valid: v.Valid}
}

// operationKindTail собирает хвост одного семейства. nil — когда заполненных полей нет вовсе:
// хвост тогда не рождается, и байты шага остаются ровно теми, что были до волны.
func operationKindTail(tag string, pairs ...operationKindPair) []any {
	filled := make([]operationKindPair, 0, len(pairs))
	for _, p := range pairs {
		if p.valid {
			filled = append(filled, p)
		}
	}
	if len(filled) == 0 {
		return nil
	}
	// ПОБАЙТНО по ключу, а не в порядке объявления: порядок объявления сдвинулся бы при первой же
	// дописке поля в середину списка. Сравнение строк в Go и есть побайтное — никакой
	// локале-зависимой сортировки здесь быть не должно, иначе отпечаток начнёт зависеть от машины.
	//
	// СТАБИЛЬНАЯ — не «на всякий случай». sort.Slice равные элементы упорядочивает произвольно, и
	// контракта на этот произвол между релизами Go нет: смена алгоритма в стандартной библиотеке
	// перетасовала бы пары с одинаковым ключом и увела бы замороженный hex БЕЗ ЕДИНОЙ ПРАВКИ КОДА.
	// Замороженный отпечаток не имеет права зависеть от реализации сортировки, поэтому здесь
	// SliceStable: при дубле ключа порядок задаёт объявление, а сам дубль ловится явно —
	// TestOperationKindPairKeysUniqueWithinTail в techcard_operation_kind_tail_test.go.
	sort.SliceStable(filled, func(i, j int) bool { return filled[i].key < filled[j].key })
	out := make([]any, 0, len(filled))
	for _, p := range filled {
		out = append(out, []any{p.key, p.value})
	}
	return []any{tag, out}
}

// operationKindTails — все одиннадцать хвостов волны в ЗАМОРОЖЕННОМ ПОРЯДКЕ, идущие после
// media-хвоста. Порядок и имена — навсегда.
//
// СЕМЕЙСТВА "press" ЗДЕСЬ НЕТ, и это не пропуск: под-глагол ВТО и направление припуска (0325)
// живут в ЕДИНСТВЕННОМ ВТО-хвосте выше, вместе с оборудованием, температурой, выдержкой,
// давлением, паром и проутюжильником. Один факт живёт в одном месте, один тег несёт один хвост.
// То же и с "machine".
//
// ДВЕНАДЦАТЫЙ — "work", ось «работа» (0330), и он стоит СРАЗУ ПОСЛЕ "fastening".
//
// "handwork" НЕ РОЖДАЕТСЯ ДО СИХ ПОР: оба его поля отложены. Обещанное ему ПОСЛЕДНЕЕ место
// сохраняется дословно — когда поля придут, хвост встанет ПОСЛЕ "work", тринадцатым, и по-прежнему
// последним. Это безопасно: существующие строки его всё равно не эмитят.
func operationKindTails(o *entity.TechCardOperation) []any {
	tails := [][]any{
		operationKindTail("stitching",
			opKindInt("needle_count", o.NeedleCount),
			opKindDec("needle_gauge_mm", o.NeedleGaugeMm),
			opKindStr("seam_securing", o.SeamSecuring),
			opKindDec("row_spacing_mm", o.RowSpacingMm),
			opKindDec("fullness_ratio", o.FullnessRatio),
			opKindStr("binding_style", o.BindingStyle),
			opKindStr("label_attach_stitch", o.LabelAttachStitch),
		),
		// Ключ — placement_count, а не count: имя колонки БД. См. довод у заголовка блока.
		operationKindTail("placement",
			opKindInt("placement_count", o.PlacementCount),
			opKindDec("pitch_mm", o.PitchMm),
		),
		operationKindTail("hardware",
			opKindStr("attach_method", o.AttachMethod),
			opKindStr("hole_prep", o.HolePrep),
			opKindStr("reinforcement", o.Reinforcement),
			opKindDec("foldback_mm", o.FoldbackMm),
			opKindInt("cycle_stitch_count", o.CycleStitchCount),
		),
		// print_method лежит в этом же хвосте, хотя на проводе он поле самого шага, а не блока:
		// хвосты режутся по СЕМЕЙСТВУ ФАКТОВ, а не по форме сообщения. ВТО-факты PRINT-шага идут
		// существующим press-хвостом — один факт живёт в одном месте.
		operationKindTail("print",
			opKindStr("print_method", o.PrintMethod),
			opKindStr("peel_mode", o.PeelMode),
			opKindInt("second_press_sec", o.SecondPressSec),
		),
		operationKindTail("weld",
			opKindInt("air_temperature_c", o.AirTemperatureC),
			opKindDec("feed_speed_m_min", o.FeedSpeedMMin),
		),
		// Ключ — trim_action, а не action.
		operationKindTail("trim",
			opKindStr("trim_action", o.TrimAction),
			opKindDec("residual_allowance_mm", o.ResidualAllowanceMm),
		),
		operationKindTail("thread_trim",
			opKindDec("residual_tail_max_mm", o.ResidualTailMaxMm),
		),
		// Ключ — cleaning_kind, а не kind.
		operationKindTail("clean",
			opKindStr("cleaning_kind", o.CleaningKind),
		),
		operationKindTail("inspect",
			opKindStr("coverage_mode", o.CoverageMode),
		),
		operationKindTail("wet",
			opKindStr("wet_process_kind", o.WetProcessKind),
		),
		operationKindTail("fastening",
			opKindStr("buttonhole_style", o.ButtonholeStyle),
			opKindDec("cut_length_mm", o.CutLengthMm),
			opKindStr("buttonhole_orientation", o.ButtonholeOrientation),
			opKindDec("bartack_length_mm", o.BartackLengthMm),
			opKindStr("attach_pattern", o.AttachPattern),
			opKindStr("zipper_application", o.ZipperApplication),
		),
		// ДВЕНАДЦАТЫЙ — ОСЬ «РАБОТА» (0330), сразу после "fastening", и место это ЗАМОРОЖЕНО.
		//
		// ХВОСТ УСЛОВНЫЙ, КАК И ОДИННАДЦАТЬ ВЫШЕ, И ИМЕННО ЭТО ДЕЛАЕТ ВОЛНУ НУЛЕВОЙ: колонка work
		// рождается NULL на КАЖДОЙ существующей строке (126 из 126 на проде), пара не эмитится,
		// хвост не рождается, байты кортежа существующего шага не двигаются ни на один. Это
		// доказывают замороженные golden-хексы, а не обещает комментарий.
		//
		// В ХВОСТ ВХОДИТ ТОЛЬКО ТОКЕН. Ярлык, глагол, стадия, синонимы и «версия каталога» — НИКОГДА
		// (прецедент: цвет выноски не хешируется). Иначе правка ярлыка UPDATE-миграцией объявляла бы
		// каждую подписанную карточку изменённой после подписи — правкой, которой человек не делал.
		//
		// ОБЕЩАНИЕ ПОСЛЕДНЕГО МЕСТА ЗА "handwork" СОХРАНЯЕТСЯ ДОСЛОВНО: он ещё не рождён, оба его
		// поля отложены; когда родится — встанет ПОСЛЕ "work", тринадцатым, и по-прежнему последним.
		// Занять место между ними будет уже нельзя: позиция хвоста в списке значима.
		operationKindTail("work",
			opKindStr("work", o.Work),
		),
	}
	out := make([]any, 0, len(tails))
	for _, t := range tails {
		if t != nil {
			out = append(out, t)
		}
	}
	return out
}

// operationBomQtyPairs — пары «ключ строки BOM, количество» для хвоста "bom_qty" (0334).
//
// ОТДЕЛЬНОЙ ФУНКЦИЕЙ, А НЕ ИНЛАЙНОМ, ровно затем, чтобы хвост строился ТЕМ ЖЕ помощником, что и
// двенадцать семейств выше: opKindDec гонит децимал через digestDecimal (float64 дал бы 6.0 и 6
// разными байтами в зависимости от того, что вернула БД), а operationKindTail сам отбрасывает
// пустые пары и сортирует оставшиеся побайтно по ключу.
//
// КЛЮЧ ПАРЫ — line_key СТРОКИ BOM, а не имя колонки, и это единственное отличие от соседей: здесь
// пары не перечень полей одного факта, а перечень РАЗНЫХ материалов. Оттого и Valid всегда true —
// связь без числа в BomQuantities не попадает вовсе (см. entity.OperationBomQty).
func operationBomQtyPairs(o *entity.TechCardOperation) []operationKindPair {
	if len(o.BomQuantities) == 0 {
		return nil
	}
	out := make([]operationKindPair, 0, len(o.BomQuantities))
	for _, q := range o.BomQuantities {
		out = append(out, opKindDec(q.LineKey, decimal.NullDecimal{Decimal: q.QtyPerGarment, Valid: true}))
	}
	return out
}

// assemblyInputTail проецирует упорядоченный union входов парами «вид, ключ».
//
// nil при пустом списке, а НЕ пустой срез: json.Marshal их различает, и шаг, записанный как [],
// но прочитанный как null, дал бы разный отпечаток на записи и на чтении — подпись рождалась бы
// протухшей и оставалась такой навсегда.
func assemblyInputTail(o *entity.TechCardOperation) []any {
	if len(o.AssemblyInputs) == 0 {
		return nil
	}
	out := make([]any, 0, len(o.AssemblyInputs))
	for _, in := range o.AssemblyInputs {
		kind := "piece"
		if in.Kind == entity.AssemblyInputUnit {
			kind = "unit"
		}
		out = append(out, []any{kind, in.Key})
	}
	return out
}

// equipmentProfilesTail projects the card's equipment park, or nil when it holds nothing.
//
// ПРИСУТСТВИЕ ОБЁРТКИ В ОТПЕЧАТОК НЕ ВХОДИТ — только её содержание. Пустая обёртка («заменить парк
// на пустой») и отсутствующая («старый клиент про парк не говорил») — одно и то же содержание, ноль
// профилей, и обязаны дать один отпечаток; иначе смена версии клиента сама по себе устаревала бы
// подпись. Транспортный флаг machine_fields_aware в проекции не появляется по тому же доводу: он
// говорит о ОТПРАВИТЕЛЕ, а не о карточке.
//
// СОРТИРОВКА ЗДЕСЬ, А НЕ В ЧТЕНИИ. Порядок профилей не значим: шаг ссылается на профиль по
// profile_key, поэтому перестановка списка не меняет ни одного указания цеху и не смеет двигать
// подпись. Полагаться на ORDER BY чтения нельзя — записываемый payload через него не проходит, и
// два порядка одного парка дали бы два отпечатка. Копия, а не сортировка на месте: проекция
// считается и на payload'е записи, и на модели чтения, и молча переупорядочить чужой слайс значило
// бы менять то, что будет записано.
func equipmentProfilesTail(c *entity.TechCardConstruction) any {
	if c == nil || c.EquipmentDefaults == nil {
		return nil
	}
	d := c.EquipmentDefaults
	if len(d.Machines)+len(d.Presses) == 0 {
		return nil
	}
	machines := append([]entity.TechCardMachineProfile(nil), d.Machines...)
	sort.SliceStable(machines, func(i, j int) bool { return machines[i].ProfileKey < machines[j].ProfileKey })
	presses := append([]entity.TechCardPressProfile(nil), d.Presses...)
	sort.SliceStable(presses, func(i, j int) bool { return presses[i].ProfileKey < presses[j].ProfileKey })

	// Id / TechCardId are deliberately absent: they are assigned by the store, so hashing them would
	// make the write payload's fingerprint differ from the read model's for the same content — the
	// permanent, un-clearable «changed since sign-off» this file's header warns about.
	machineRows := make([]any, 0, len(machines))
	for _, m := range machines {
		machineRows = append(machineRows, []any{
			m.ProfileKey, m.Label.String, m.MachineType, m.ThreadCount.Int32,
			m.NeedleType.String, m.NeedleSizeNm.Int32, m.BedType.String, m.Automation.String,
			m.ThreadTension.String, m.ThreadTensionNote.String, m.AttachmentKind.String,
			digestDecimal(m.StitchesPerCm), digestDecimal(m.StitchWidthMm), m.Note.String,
		})
	}
	pressRows := make([]any, 0, len(presses))
	for _, p := range presses {
		pressRows = append(pressRows, []any{
			p.ProfileKey, p.Label.String, p.PressEquipment, p.PressOperationType.String,
			p.PressTemperatureC.Int32, p.PressDwellSec.Int32, digestDecimal(p.PressPressureNCm2),
			// Тот же трёхзначный пар, что и на шаге, и парой по той же причине.
			[]any{p.PressSteam.Valid, p.PressSteam.Bool}, p.PressCloth.String, p.Note.String,
		})
	}
	return []any{"equipment", machineRows, pressRows}
}

// materialsProjection fingerprints what the card BUYS: which article, at what price, in what
// quantity terms.
//
// DELIBERATELY ABSENT — purpose / purpose_note / is_sample (0265), and kind / kind_note (0278) on
// exactly the same grounds. They classify a line that already exists; they do not change the
// article, the price or the consumption, so on the same reasoning as price_source they are metadata
// about a value and must not stale a sign-off whose value did not change. The same holds for
// wastage_source / wastage_lay_count / wastage_applied_at (0296): the SIGNED value is
// wastage_percent itself (already below); where the number came from is audit about it, and folding
// the provenance in would restamp every card at the first save after deploy — the wall-of-stale
// failure this paragraph exists to prevent. The concrete cost of
// folding them in would be paid immediately and by everyone: every pre-0265 line is deliberately
// unsorted and every pre-0278 line deliberately unclassified, so the operator's first sorting pass
// over an approved card would mark its MATERIALS approval stale on every single card at once — a
// wall of "changed since sign-off" that means nothing and trains people to ignore the signal that
// does. Adding them later is a digest rebase (see costingProjection's placeholder note) and needs
// the same care.
//
// THE DAY THAT CHANGES — and it is a specific day, not a vibe: the moment a kind value becomes an
// INPUT TO A DERIVATION rather than a grouping. Today `kind` only buckets lines for a screen. If it
// ever picks a costing rule, a consumption unit, an assembly step or anything the card's OUTPUT
// depends on, then "these buttons are now snaps" changes what is made, the MATERIALS signature must
// move with it, and the field must be folded in — as a TAGGED PAIR TAIL, emitted only when filled
// (see materialsTails below, and constructionProjection's cut_symmetry note for why an unconditional
// element restamps every card in the database at deploy time instead of only the ones somebody
// actually answered).
//
// ЭТОТ АБЗАЦ РАНЬШЕ ПРЕДПИСЫВАЛ «ПОЗИЦИОННЫЙ ХВОСТ», И ИСПРАВЛЕНО ЭТО НЕ РАДИ СЛОВА. Позиционный
// хвост закрывает ровно половину задачи: байты строки, где поле пусто, он действительно не двигает,
// но СОСТАВ хвоста замораживает навсегда — дописать в уже рождённый позиционный массив второе поле
// нельзя ни в конец, ни в середину, потому что длина и порядок входят в байты. Расплату за это уже
// увидели на шаге: дописать поле было некуда, и под тегом "press" завелись ДВА хвоста разной формы,
// различимые только по форме (довод — у кортежа шага в constructionProjection). Поэтому здесь, как
// у упаковочного листа и у одиннадцати семейств шага, хвост ПАРНЫЙ: ["тег", [[имя колонки БД,
// значение], …]]. Состав пар расти умеет, голова — нет, и это ровно то разделение, которое нужно.
func materialsProjection(tc *entity.TechCardInsert) any {
	items := make([]any, 0, len(tc.BomItems))
	for i := range tc.BomItems {
		items = append(items, materialsRow(&tc.BomItems[i]))
	}
	return items
}

// materialsRow — ГОЛОВА ИЗ СЕМНАДЦАТИ ЗАМОРОЖЕННЫХ ПОЗИЦИЙ плюс тегированные парные хвосты, одним
// строителем.
//
// ЗАЧЕМ ОТДЕЛЬНАЯ ФУНКЦИЯ, А НЕ ТЕЛО ЦИКЛА, — тот же довод, что у packagingRow. Строитель есть
// ЕДИНСТВЕННОЕ место, где перечислены подписываемые поля строки BOM, поэтому именно его открывает
// тот, кто заводит новое поле. Пока сборка жила прямо в цикле, «дописать поле» читалось как
// «дописать позицию» — и было бы сделано безусловно, потому что так короче на одну строку; цена
// этой короткости — новый отпечаток у КАЖДОЙ строки BOM в базе и стена «изменено после подписи»
// в момент выкатки. Вынос строителя не косметика: он делает дописку хвостом дешевле дописки
// позицией, а дешёвое и делают.
//
// ГОЛОВА ЗАМОРОЖЕНА, И ЭТОЙ ПРАВКОЙ ЕЁ БАЙТЫ НЕ ДВИНУЛИСЬ: сегодня ни одно семейство хвоста не
// рождается, поэтому отпечаток строки до и после рефакторинга совпадает побайтно — это и
// проверяется замороженным hex (materialsGoldDigestHex), снятым на коммите ДО правки. Переписывать
// саму голову в пары было бы правкой ради единообразия ценой волны пере-утверждения на всю базу:
// пары нужны там, где СОСТАВ будет расти, а не там, где он уже зафиксирован.
func materialsRow(b *entity.TechCardBomItem) []any {
	out := []any{
		b.LineKey, string(b.Section), b.Name, b.Supplier.String, b.SupplierRef.String,
		b.Color.String, b.Composition.String, b.Spec.String, b.Unit.String,
		digestDecimal(b.UnitPrice), b.Currency.String, b.Comment.String,
		digestDecimal(b.FabricWidth), digestDecimal(b.FabricWeightGsm), b.FabricDirection.String,
		digestDecimal(b.WastagePercent), b.MaterialId.Int64,
	}
	return append(out, materialsTails(b)...)
}

// materialsTails — хвосты строки BOM в ЗАМОРОЖЕННОМ ПОРЯДКЕ, той же формы и ТЕМ ЖЕ строителем, что
// у шага и у упаковочного листа: пара рождается только у заполненного поля, пустой набор пар хвоста
// не рождает вовсе, пары внутри хвоста отсортированы побайтно по ключу.
//
// ОДИН СТРОИТЕЛЬ НА ВСЮ ПРОЕКЦИЮ, А НЕ КОПИЯ ДЛЯ МАТЕРИАЛОВ. Копия — это второе место, где живут
// правила сортировки и рождения пары, и оно разъедется с первым молча, а расплатой будет
// разъехавшийся отпечаток. Имя operationKindTail историческое; читать его следует как «хвост
// парной формы».
//
// СЕМЕЙСТВО "count" — СЧЁТНАЯ НОРМА СЛОТА (0333), первое и пока единственное. Порядок работ был
// предписан и соблюдён: механизм хвоста завёлся ОТДЕЛЬНЫМ шагом, поля — этим. Строка, у которой
// счётная норма не заполнена, хвоста не рождает вовсе, поэтому байты подавляющего большинства
// строк BOM в базе не двигаются ни на один, а отпечаток уезжает РОВНО у тех строк, где количество
// действительно проставили. Обратный порядок — поле раньше механизма — стоил бы объявления всех
// утверждённых подписей MATERIALS устаревшими в момент выкатки, у каждой карточки в базе и ни за
// что.
//
// ПОЧЕМУ СЧЁТНАЯ НОРМА ВООБЩЕ ПОДПИСЫВАЕТСЯ, хотя kind и purpose из проекции исключены намеренно.
// Критерий тот же, что записан у materialsProjection: подписывается то, что карточка ПОКУПАЕТ.
// qty_per_garment и spare_qty не группируют строки для экрана — они прямо задают закупаемое
// количество (entity.CountablePairTotal), то есть входят и в себестоимость, и в потребность цеха.
// Утверждение MATERIALS под «шесть пуговиц плюс одна в пакетик» не имеет права читаться как
// действительное под «две»; ровно для этого различия секция и подписывается.
//
// ТЕГ "count" И КЛЮЧИ ПАР ЗАМОРОЖЕНЫ С ЭТОГО КОММИТА: имя тега и имена ключей входят в байты
// отпечатка, и первая же подписанная карточка со счётной нормой делает переименование невозможным.
// Ключи — ИМЕНА КОЛОНОК БД (qty_per_garment, spare_qty), тем же правилом, что у placement_count у
// шага: имя колонки ищется грепом, придуманный синоним — нет.
//
// ПАРА ПИШЕТСЯ ПО Valid, А НЕ ПО «НЕ НОЛЬ». digestDecimal отдаёт "" на NULL и "0" на нуле, то есть
// «сколько пуговиц — не сказано» и «пуговиц ноль» различаются написанием уже сейчас; правило по
// Valid сохраняет это различие и в хвосте: у незаполненного поля пары нет вовсе, у нуля пара есть
// со значением "0". Ноль пуговиц — утверждение технолога, а не молчание, и подпись под ответом
// не имеет права читаться как подпись под неотвеченным вопросом.
//
// ИМЯ ТЕГА ЗАМОРАЖИВАЕТСЯ В МОМЕНТ ПЕРВОГО РОЖДЕНИЯ ХВОСТА, а не сейчас: пока список пуст, тега в
// байтах нет, и следующая задача вольна назвать семейство так, как ему подходит. После первой же
// подписи переименовать его будет уже нельзя — имя ключа и имя тега входят в отпечаток.
func materialsTails(b *entity.TechCardBomItem) []any {
	tails := [][]any{
		operationKindTail("count",
			opKindDec("qty_per_garment", b.QtyPerGarment),
			opKindDec("spare_qty", b.SpareQty),
		),
		// ЦВЕТ PANTONE ВХОДИТ В ПОДПИСЬ, в отличие от цвета выноски: код PANTONE это то, во что
		// красят, а не то, чем различают строки на экране. Строка, у которой сменили код, — другая
		// инструкция поставщику.
		//
		// ПАРОЙ, А НЕ ПОЗИЦИЕЙ, поэтому у строк без кода — а это каждая существующая — хвост не
		// рождается вовсе и отпечаток не двигается.
		operationKindTail("colour", opKindStr("pantone", b.Pantone)),
	}
	out := make([]any, 0, len(tails))
	for _, t := range tails {
		if t != nil {
			out = append(out, t)
		}
	}
	return out
}

// colourProjection covers the colour decision the STYLE owns: which fabric (and fusing) each cut
// piece takes in each colourway. The colourways themselves, and their lab dips, belong to the
// colourway write path and are versioned there — see the note on TechCardSectionDigests.
func colourProjection(tc *entity.TechCardInsert) any {
	out := make([]any, 0, len(tc.Pieces))
	for _, p := range tc.Pieces {
		mats := make([]any, 0, len(p.Materials))
		for _, m := range p.Materials {
			mats = append(mats, []any{
				m.ColorwayID, m.BomLineKey, m.FusingBomLineKey, m.Note.String,
			})
		}
		out = append(out, []any{p.LineKey, mats})
	}
	return out
}

func labelsProjection(tc *entity.TechCardInsert) any {
	labels := make([]any, 0, len(tc.Labels))
	for _, l := range tc.Labels {
		labels = append(labels, []any{
			string(l.LabelType), l.Content.String, l.Placement.String,
			l.Attachment.String, l.Size.String, l.Note.String, l.BomItemId.Int32,
		})
	}
	return labels
}

// packagingProjection — упаковочный лист: ГОЛОВА из десяти замороженных позиций плюс тегированные
// ПАРНЫЕ хвосты, ровно та же дисциплина, что у кортежа шага.
//
// ЗАЧЕМ ЗДЕСЬ ВООБЩЕ ХВОСТ. До этой правки проекция была плоским позиционным массивом без единого
// различителя — то есть структурой, в которую нельзя дописать поле: одиннадцатый элемент,
// поставленный безусловно, сдвинул бы отпечаток КАЖДОГО упаковочного листа в базе и объявил бы все
// утверждённые подписи PACKAGING устаревшими в момент выкатки. А T8b заводит листу пять полей
// сразу (fold_scheme, folded_w_mm, folded_h_mm, face_out, metal_pins_prohibited). Механизм хвоста
// обязан появиться РАНЬШЕ полей — иначе первое же поле придётся вносить ценой волны.
//
// ГОЛОВА ЗАМОРОЖЕНА, И ЭТОЙ ПРАВКОЙ ЕЁ БАЙТЫ НЕ ДВИНУЛИСЬ: сегодня ни одно семейство хвоста не
// рождается, поэтому отпечаток листа до и после рефакторинга совпадает побайтно — это и
// проверяется замороженным hex (packagingGoldDigestHex). Переписывать голову в пары было бы
// правкой ради единообразия, а голова кортежа шага тоже осталась позиционной: пары нужны там, где
// СОСТАВ будет расти, а не там, где он уже зафиксирован.
// ПУСТОЙ ЛИСТ = ОТСУТСТВУЮЩИЙ ЛИСТ, и это не косметика. Обёртка, в которой не заполнено ни одного
// поля, — то же содержание, что и «упаковки нет», а до сведения эти два состояния давали РАЗНЫЕ
// отпечатки: массив из десяти пустых позиций против null. Карточка, у которой кто-то однажды открыл
// и закрыл вкладку упаковки, ничего не заполнив, объявляла бы свою секцию PACKAGING изменённой без
// единого изменения. Довод и выбор каноничного написания — у blankAsAbsent, решение то же, что уже
// принято у парка оборудования.
func packagingProjection(tc *entity.TechCardInsert) any {
	return blankAsAbsent(tc.Packaging, packagingRow)
}

// packagingRow — голова плюс хвосты одним строителем: он же прогоняется на НУЛЕВОМ листе, чтобы
// вывести пустоту, поэтому поле T8b попадёт в это сведение само, без второго списка полей.
func packagingRow(p *entity.TechCardPackaging) []any {
	out := []any{
		p.FoldingMethod.String, p.Polybag.String, p.BagSticker.String, p.Inserts.String,
		p.UnitsPerBox.Int32, p.BoxMarking.String, p.BoxDimensions.String,
		p.WeightNetGrams.Int32, p.WeightGrossGrams.Int32, p.Notes.String,
	}
	return append(out, packagingTails(p)...)
}

// packagingTails — хвосты упаковочного листа в ЗАМОРОЖЕННОМ ПОРЯДКЕ, той же формы и тем же
// строителем, что у шага: ["тег", [[имя колонки БД, значение], …]], пара только у заполненного
// поля, пустой набор пар хвоста не рождает, пары отсортированы побайтно.
//
// ОДИН СТРОИТЕЛЬ НА ВСЮ ПРОЕКЦИЮ, А НЕ КОПИЯ ДЛЯ УПАКОВКИ. Копия — это второе место, где живут
// правила сортировки и рождения пары, и оно разъедется с первым молча, а расплатой будет
// разъехавшийся отпечаток. Имя operationKindTail историческое; читать его следует как «хвост
// парной формы».
//
// СЕГОДНЯ СПИСОК ПУСТ, и это не заготовка впрок, а порядок работ, предписанный планом: T8a заводит
// упаковке МЕХАНИЗМ хвоста, T8b — поля. Пустой список означает ровно то, что нужно: байты
// сегодняшнего листа не двигаются, а первое же поле T8b ляжет в хвост "fold" и сдвинет отпечаток
// только тех листов, где его реально заполнили.
func packagingTails(p *entity.TechCardPackaging) []any {
	tails := [][]any{
		// T8b: operationKindTail("fold", opKindStr("fold_scheme", p.FoldScheme), …)
	}
	out := make([]any, 0, len(tails))
	for _, t := range tails {
		if t != nil {
			out = append(out, t)
		}
	}
	return out
}

// costingProjection fingerprints what the card COSTS: the manual articles, the declared typical
// run, and — since the range-average basis (T6) — the DECLARED SIZE RANGE the standard cost
// averages over.
//
// WHY size_ids are in here even though the range lives on the card header. The range is not
// metadata about the costing, it is an INPUT TO THE DERIVATION: a size-graded material norm
// enters the style cost as Σ norm / |range| over exactly that set
// (entity.TechCardColorwayUsage.RangeAverageTotal), so adding or removing a size reprices every
// colourway of the style. Leaving it out would give exactly the failure the whole mechanism
// exists to prevent — a costing sign-off staying green over a number that changed. That is the
// same test materialsProjection sets for `kind`: the day a field becomes an input to a
// derivation rather than a grouping, it must join the signature.
//
// SORTED, because only MEMBERSHIP is the input: Σ/n is order-blind, so reordering the declared
// range must not restamp a signature, while adding or removing a size must. Hashing the stored
// order would let a cosmetic reshuffle read as «changed since sign-off».
//
// base_sample_size_id LEFT the projection in the same change, unconditionally: it is a reference
// «размер образца» now, not an input — re-pointing it moves no figure, so keeping it hashed
// would restamp signatures over nothing. Swapping the basis element restamps every stored
// COSTING digest at deploy — every approved costing sign-off goes stale at once. That is honest,
// not collateral damage (owner: «переподпишем»): the basis of the number under those signatures
// changed in the same commit, from «the base size's own norm» to «the simple average over the
// declared range», so no existing approval describes the current figure. The positional-tail
// trick (see constructionProjection) would have bought nothing here — there is no card for which
// the two bases agree by construction.
// costingRow — блок ручных статей себестоимости.
//
// Positions 2 and 3 held hardware_cost / packaging_cost until Phase 2 removed them. They stay
// as the canonical empty placeholder ("" = digestDecimal of an unset decimal) so the digest of
// a card that never had those scalars is BYTE-IDENTICAL to what its approver signed — the
// removal must not mass-stale every approved costing sign-off (review S1, digest-rebase). A
// card whose scalar WAS migrated changes digest legitimately: its costing content moved.
func costingRow(c *entity.TechCardCosting) []any {
	return []any{
		digestDecimal(c.CmtCost), "", "", digestDecimal(c.LogisticsCost),
		digestDecimal(c.OverheadCost), digestDecimal(c.DefectPercent), c.Currency.String, c.Notes.String,
		digestDecimal(c.TargetMarginPct),
	}
}

func costingProjection(tc *entity.TechCardInsert) any {
	// ПУСТОЙ БЛОК = ЕГО ОТСУТСТВИЕ (см. blankAsAbsent): «ручных статей не заявлено» — одно
	// содержание, и написаний у него должно быть одно. Отдельно важно здесь: пустую обёртку рождает
	// не только клиент, но и серверный путь релиза (production_run_release_cost), поэтому без
	// сведения отпечаток COSTING зависел бы от того, каким путём карточка приехала.
	costing := blankAsAbsent(tc.Costing, costingRow)
	qty := make([]any, 0, len(tc.SizeQuantities))
	for _, q := range tc.SizeQuantities {
		qty = append(qty, []any{q.SizeId, q.OrderQty})
	}
	rangeIds := append([]int(nil), tc.SizeIds...)
	sort.Ints(rangeIds)
	rangeVals := make([]any, 0, len(rangeIds))
	for _, id := range rangeIds {
		rangeVals = append(rangeVals, id)
	}
	out := []any{costing, qty, rangeVals}
	// A TAIL, NOT A SLOT — the same device constructionProjection uses, and for the same reason:
	// json.Marshal encodes []any POSITIONALLY, so a fourth element written unconditionally (even as
	// "") would move the fingerprint of EVERY card in the database and declare every approved
	// COSTING sign-off stale at the moment of deploy, before anybody had touched anything.
	//
	// WHAT THIS COVERS AND WHY IT BELONGS IN THE SIGNATURE. Since Ф1 the standard cost is derived
	// from two things the card's own write does not carry: the MEASURED AREAS of its cut pieces
	// (0297) and the recipe's statement of WHICH FABRIC each piece is cut from. Both change the
	// number under the signature. Leaving them out would let a re-parse of the patterns, or a piece
	// moved from the main fabric to the lining, reprice an approved card while its COSTING sign-off
	// stayed green — signing one document and shipping another.
	//
	// Empty (no areas AND no piece-bound recipe rows) appends nothing, so the only cards that go
	// stale are the ones where somebody actually measured or assigned. Today that set is empty:
	// nothing writes areas yet, and the piece-assignment half moves only cards whose COSTING
	// approval was already describing a figure it could not see.
	if tc.DerivedCostInputsDigest != "" {
		out = append(out, tc.DerivedCostInputsDigest)
	}
	return out
}

// dec renders a nullable decimal as a canonical string, so 1.50 and 1.5 — which the DB may return
// differently from what the client sent — cannot produce two different fingerprints.
func digestDecimal(d decimal.NullDecimal) string {
	if !d.Valid {
		return ""
	}
	return d.Decimal.String()
}
