package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/encoding/protojson"
)

// Ф1.3 — ВСЁ СОДЕРЖИМОЕ АРХИВА ТЕХ-КАРТЫ, КРОМЕ manifest.json И card.json.
//
// Разделение труда во всём файле одно, и оно важнее любой отдельной функции:
//
//   - ОШИБКА (возврат error) — это отказ ИНФРАСТРУКТУРЫ: база не ответила, локальный диск не
//     принял файл, карта-аномалия перевалила за потолок формата. Такой экспорт не должен
//     закончиться архивом вовсе: архив, у которого молча нет половины медиа, потому что упал
//     бакет, неотличим от карточки, у которой их и не было.
//   - ОТСУТСТВИЕ ДАННЫХ — это ПУСТОЙ сайдкар, а не ошибка и не дыра. Карточка без размерной
//     таблицы даёт `{"cells":[]}`, карточка без медиа не даёт папки media/ вовсе.
//   - БИТАЯ ССЫЛКА — это ДЫРА (techcardarchive.ExportHole) с кодом из закрытого словаря
//     internal/techcardarchive/reasons.go, после которой сбор ПРОДОЛЖАЕТСЯ. Удалённый из
//     каталога материал, потерянный объект картинки, компонент сборки, чью карточку снесли, —
//     всё это законные состояния живой базы, и экспорт обязан довезти остальное и честно
//     назвать потерянное.
//
// Кодов причин выдумывать нельзя: словарь закрыт, и добавление кода — это изменение формата
// (см. заголовок reasons.go). Там, где закрытый словарь молчит, здесь стоит ЛОГ, а не
// самодельная причина.

// archiveSidecars — собранное содержимое архива, кроме manifest.json и card.json. Ф1.5 берёт
// отсюда и тела сайдкаров, и файлы, и дыры, и считает по ним contents-счётчики манифеста.
//
// ВЫЗЫВАЮЩИЙ ОБЯЗАН ВЫЗВАТЬ Close. Двоичные файлы (медиа и выкройки) НЕ лежат в памяти: одно
// легальное видео на карточке весит десятки мегабайт, а потолок формата — гигабайт, поэтому байты
// уходят во временный каталог, а Close его сносит целиком. Каталог создаётся ЛЕНИВО — у карточки
// без файлов его нет вовсе, и Close на такой структуре ничего не делает.
type archiveSidecars struct {
	// SizeChart — sizechart.json. Обе оси именами (FORMAT.md §5.1); пустой Cells = таблицы нет.
	SizeChart techcardarchive.SizeChart
	// Assembly — assembly.json. Пустой список = у стиля нет сборочной ведомости.
	Assembly []techcardarchive.AssemblyLink
	// Colorways — colorways.json, СПРАВОЧНАЯ полезная нагрузка без денег (§5.3).
	Colorways []techcardarchive.ColorwayPayload
	// Materials — materials/index.json: паспорта ровно тех артикулов, на которые карта ссылается.
	Materials []techcardarchive.MaterialPassport
	// Media / Patterns — индексы двоичных файлов; сами байты в Blobs.
	Media    []techcardarchive.MediaIndexEntry
	Patterns []techcardarchive.PatternIndexEntry
	// Markers — markers/index.json; сами блобы в MarkerFiles.
	Markers []techcardarchive.MarkerIndexEntry

	// MarkerFiles — файлы markers/<slug>-<n>.json, protojson common.TechCardMarker целиком.
	// В памяти, в отличие от Blobs: раскладка ограничена 2 МиБ, и их единицы.
	MarkerFiles []archiveJSONFile
	// Blobs — файлы media/<sha256>.<ext> и patterns/<sha256>.<ext>, отложенные на диск и уже
	// дедуплицированные по содержимому: одна и та же картинка в двух слотах — один файл.
	Blobs []archiveBlob

	// SizeNames — ИМЕНА размеров для каждого id, который встретился при сборке сайдкаров, включая
	// id внутри блобов раскладок. Ф1.5 обязана СЛИТЬ этот набор с размерами самой card.json в
	// manifest.id_maps.sizes: импорт переотображает КАЖДЫЙ size_id внутри блоба раскладки через ту
	// же таблицу имён (FORMAT.md §5.7), а размер, который лежит только в раскладке смешанного
	// настила, в card.json может не встретиться ни разу.
	SizeNames map[int]string

	// Holes — дыры ЭТОЙ половины экспорта. Ф1.5 сливает их с дырами Ф1.2 в manifest.export_holes.
	Holes []techcardarchive.ExportHole

	spool *archiveSpool
}

// Close убирает временные файлы. Идемпотентен и безопасен на nil-приёмнике, чтобы `defer` можно
// было поставить сразу после вызова сборщика, не разбирая, чем он закончился.
func (a *archiveSidecars) Close() error {
	if a == nil {
		return nil
	}
	return a.spool.close()
}

// archiveJSONFile — один готовый JSON-файл архива: имя записи и её байты.
type archiveJSONFile struct {
	// Name — имя записи ОТ КОРНЯ архива, "markers/m-1.json" (FORMAT.md §1.1).
	Name string
	Data []byte
}

// archiveBlob — один двоичный файл архива: имя записи, sha256 содержимого, размер и путь к
// отложенной на диск копии. Имя строится из sha256 — отсюда и целостность, и дедуп внутри архива,
// и дедуп на импорте (media.content_hash), одним соглашением на всё (FORMAT.md §1.1).
type archiveBlob struct {
	Name   string
	SHA256 string
	Size   int64
	path   string
}

// Open открывает отложенную копию для записи в zip. Вызывающий закрывает.
func (b archiveBlob) Open() (io.ReadCloser, error) {
	f, err := os.Open(b.path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errArchiveSpoolFailed, err)
	}
	return f, nil
}

var (
	// errArchiveSpoolFailed — отказ ЛОКАЛЬНОГО хранилища: не создался временный каталог, не
	// записался файл. Это инфраструктура, а не «данных нет»: превратить её в дыру значило бы
	// отдать оператору архив без картинок и с объяснением «объект не читается», которое неверно.
	errArchiveSpoolFailed = errors.New("tech card archive: cannot spool a file to local storage")
	// errArchiveContentTooLarge — суммарный объём файлов перевалил за потолок формата
	// (techcardarchive.MaxUncompressedBytes). ОТКАЗ, а не усечение: архив без части файлов, но с
	// полным индексом — это архив, который импорт объявит битым, и правильно сделает.
	//
	// Считается ЗДЕСЬ, а не только в Ф1.5, потому что байты материализует этот файл: проверка
	// «после сборки» означала бы гигабайты, уже уехавшие во временный каталог. Число берётся из
	// того же format.go, поэтому две проверки не могут разойтись.
	errArchiveContentTooLarge = errors.New("tech card archive: content exceeds the format ceiling")
)

// archiveSpool — временный каталог под двоичные файлы архива плюс дедуп по содержимому.
type archiveSpool struct {
	dir   string
	total int64
	// byName — уже отложенные файлы по имени записи (prefix+sha256+ext). Имя содержит хеш, так
	// что совпадение имени и есть совпадение содержимого.
	byName map[string]archiveBlob
	// order — порядок первого появления, чтобы состав архива не зависел от обхода map.
	order []string
}

func newArchiveSpool() *archiveSpool {
	return &archiveSpool{byName: make(map[string]archiveBlob)}
}

func (sp *archiveSpool) ensureDir() error {
	if sp.dir != "" {
		return nil
	}
	dir, err := os.MkdirTemp("", "grbpwr-techcard-archive-")
	if err != nil {
		return fmt.Errorf("%w: %w", errArchiveSpoolFailed, err)
	}
	sp.dir = dir
	return nil
}

// add вычитывает поток во временный файл, считая по дороге sha256, и возвращает готовый blob.
//
// Ошибка ЧТЕНИЯ ИСТОЧНИКА возвращается как есть — вызывающий делает из неё дыру; отказ диска и
// перебор потолка возвращаются с errArchiveSpoolFailed / errArchiveContentTooLarge и обязаны
// подниматься наверх как отказ всего экспорта. Различать их здесь, а не у вызывающего, потому что
// только здесь видно, кто из двух потоков сломался.
func (sp *archiveSpool) add(prefix, ext string, r io.Reader) (archiveBlob, error) {
	if err := sp.ensureDir(); err != nil {
		return archiveBlob{}, err
	}
	f, err := os.CreateTemp(sp.dir, "blob-")
	if err != nil {
		return archiveBlob{}, fmt.Errorf("%w: %w", errArchiveSpoolFailed, err)
	}
	tmpPath := f.Name()
	// Убрать за собой на ЛЮБОМ выходе, кроме успешного: успешный путь обнуляет tmpPath.
	defer func() {
		if tmpPath != "" {
			f.Close()
			os.Remove(tmpPath)
		}
	}()

	// Потолок читается на ходу: LimitReader на остаток бюджета + 1 байт, чтобы перебор было видно
	// по факту прочитанного, а не по заявленному размеру объекта (заявленный — это утверждение
	// хранилища о себе).
	budget := int64(techcardarchive.MaxUncompressedBytes) - sp.total
	if budget < 0 {
		budget = 0
	}
	h := sha256.New()
	written, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, budget+1))
	if err != nil {
		// Источник (сеть/бакет) или диск — здесь не различить; отдаём как есть, и вызывающий
		// делает дыру. Отказ диска на этом шаге редок и всё равно проявится на следующем файле
		// созданием временного файла.
		return archiveBlob{}, err
	}
	if written > budget {
		return archiveBlob{}, fmt.Errorf("%w: %d bytes already spooled, ceiling %d",
			errArchiveContentTooLarge, sp.total, techcardarchive.MaxUncompressedBytes)
	}
	if err := f.Close(); err != nil {
		return archiveBlob{}, fmt.Errorf("%w: %w", errArchiveSpoolFailed, err)
	}

	sum := hex.EncodeToString(h.Sum(nil))
	name := prefix + sum + ext
	if existing, ok := sp.byName[name]; ok {
		// Те же байты уже в архиве: второй слот получает то же имя файла и ту же сумму, а копия
		// на диске не заводится. Это и есть дедуп из FORMAT.md §1.1.
		return existing, nil
	}
	blob := archiveBlob{Name: name, SHA256: sum, Size: written, path: tmpPath}
	sp.byName[name] = blob
	sp.order = append(sp.order, name)
	sp.total += written
	tmpPath = "" // файл принят, удалять нечего
	return blob, nil
}

// blobs возвращает отложенные файлы в порядке первого появления.
func (sp *archiveSpool) blobs() []archiveBlob {
	if sp == nil {
		return nil
	}
	out := make([]archiveBlob, 0, len(sp.order))
	for _, name := range sp.order {
		out = append(out, sp.byName[name])
	}
	return out
}

func (sp *archiveSpool) close() error {
	if sp == nil || sp.dir == "" {
		return nil
	}
	dir := sp.dir
	sp.dir = ""
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("%w: %w", errArchiveSpoolFailed, err)
	}
	return nil
}

// collectArchiveSidecars собирает всё содержимое архива, кроме manifest.json и card.json.
//
// Словари размеров и мерок читаются ОДИН раз на весь экспорт и передаются вниз: размер называют
// шесть разных сайдкаров, и запрос на каждый из них превратил бы выгрузку одной карточки в
// десятки round trip'ов за одной и той же таблицей.
func (s *Server) collectArchiveSidecars(ctx context.Context, card *entity.TechCard) (out *archiveSidecars, err error) {
	if card == nil {
		return nil, fmt.Errorf("tech card archive: card is required")
	}
	di, err := s.repo.Cache().GetDictionaryInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("tech card archive: load dictionary: %w", err)
	}
	sizes := make(map[int]string, len(di.Sizes))
	for _, sz := range di.Sizes {
		sizes[sz.Id] = sz.Name
	}
	measurements := make(map[int]string, len(di.Measurements))
	for _, m := range di.Measurements {
		measurements[m.Id] = m.Name
	}

	sc := &archiveSidecars{spool: newArchiveSpool(), SizeNames: make(map[int]string)}
	defer func() {
		if err != nil {
			// Частично отложенные файлы за собой не оставляем: экспорт закончился отказом, и
			// временный каталог никто больше не откроет.
			sc.Close()
			out = nil
		}
	}()

	chart, holes, err := s.collectArchiveSizeChart(ctx, card, sizes, measurements, sc)
	if err != nil {
		return nil, err
	}
	sc.SizeChart = chart
	sc.Holes = append(sc.Holes, holes...)

	assembly, holes, err := s.collectArchiveAssembly(ctx, card, sizes, sc)
	if err != nil {
		return nil, err
	}
	sc.Assembly = assembly
	sc.Holes = append(sc.Holes, holes...)

	colorways, holes := collectArchiveColorways(card, sizes, sc)
	sc.Colorways = colorways
	sc.Holes = append(sc.Holes, holes...)

	materials, holes, err := s.collectArchiveMaterials(ctx, card)
	if err != nil {
		return nil, err
	}
	sc.Materials = materials
	sc.Holes = append(sc.Holes, holes...)

	media, holes, err := s.collectArchiveMedia(ctx, card, sc.spool)
	if err != nil {
		return nil, err
	}
	sc.Media = media
	sc.Holes = append(sc.Holes, holes...)

	patterns, holes, err := s.collectArchivePatterns(ctx, card, sizes, sc, sc.spool)
	if err != nil {
		return nil, err
	}
	sc.Patterns = patterns
	sc.Holes = append(sc.Holes, holes...)

	markers, markerFiles, holes, err := s.collectArchiveMarkers(ctx, card, sizes, sc)
	if err != nil {
		return nil, err
	}
	sc.Markers = markers
	sc.MarkerFiles = markerFiles
	sc.Holes = append(sc.Holes, holes...)

	sc.Blobs = sc.spool.blobs()
	return sc, nil
}

// ────────────────────────────── sizechart.json ──────────────────────────────

// collectArchiveSizeChart переводит размерную таблицу стиля в СВОЙ тип формата, где обе оси —
// имена: size.name и measurement_name.name уникальны в любом инстансе, а их id — нет. Это и есть
// причина, по которой файл не сырой protojson: protojson common.StyleSizeChart физически не может
// положить имя в int32-поле (FORMAT.md §5.1).
//
// style_id и lock_version не едут вовсе — это идентичность экспортирующего инстанса.
func (s *Server) collectArchiveSizeChart(ctx context.Context, card *entity.TechCard,
	sizes, measurements map[int]string, sc *archiveSidecars,
) (techcardarchive.SizeChart, []techcardarchive.ExportHole, error) {
	out := techcardarchive.SizeChart{Cells: []techcardarchive.SizeChartCell{}}
	chart, err := s.repo.TechCards().GetStyleSizeChart(ctx, card.Id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Стиля нет в этом чтении — гонка с удалением. Пустая таблица честнее отказа: карта
			// уже прочитана целиком, и одна её пустая часть не повод потерять остальные.
			return out, nil, nil
		}
		return out, nil, fmt.Errorf("tech card archive: load size chart: %w", err)
	}

	var holes []techcardarchive.ExportHole
	for _, c := range chart.Cells {
		sizeName, ok := sizes[c.SizeID]
		if !ok {
			holes = append(holes, archiveHole(techcardarchive.EntitySize, fmt.Sprintf("size_id=%d", c.SizeID),
				techcardarchive.ReasonSizeUnknown,
				"the size is not in the exporting instance's dictionary; the row is not exported"))
			continue
		}
		name, ok := measurements[c.MeasurementNameID]
		if !ok {
			// Своя причина, а не size_unknown: у этого файла ДВЕ именные оси, и оператор,
			// которому про мерку сказали «размер неизвестен», пойдёт смотреть не тот словарь.
			holes = append(holes, archiveHole(techcardarchive.EntityMeasurement,
				fmt.Sprintf("measurement_name_id=%d", c.MeasurementNameID),
				techcardarchive.ReasonMeasurementUnknown,
				"the measurement is not in the exporting instance's dictionary; the row is not exported"))
			continue
		}
		sc.rememberSize(c.SizeID, sizeName)
		out.Cells = append(out.Cells, techcardarchive.SizeChartCell{
			SizeName: sizeName, Measurement: name, Value: c.Value.String(),
		})
	}

	if chart.GradeBaseSizeID > 0 {
		if name, ok := sizes[chart.GradeBaseSizeID]; ok {
			sc.rememberSize(chart.GradeBaseSizeID, name)
			out.GradeBaseSizeName = name
		} else {
			holes = append(holes, archiveHole(techcardarchive.EntitySize, fmt.Sprintf("size_id=%d", chart.GradeBaseSizeID),
				techcardarchive.ReasonSizeUnknown,
				"the grade base size is not in the exporting instance's dictionary; the grade rule travels without a base"))
		}
	}
	for _, st := range chart.GradeSteps {
		name, ok := measurements[st.MeasurementNameID]
		if !ok {
			holes = append(holes, archiveHole(techcardarchive.EntityMeasurement,
				fmt.Sprintf("measurement_name_id=%d", st.MeasurementNameID),
				techcardarchive.ReasonMeasurementUnknown,
				"the measurement is not in the exporting instance's dictionary; the grade step is not exported"))
			continue
		}
		out.GradeSteps = append(out.GradeSteps, techcardarchive.SizeChartGradeStep{
			Measurement: name, Step: st.Step.String(),
		})
	}
	return out, holes, nil
}

// ────────────────────────────── assembly.json ──────────────────────────────

// collectArchiveAssembly собирает сборочную ведомость стиля: вспомогательные изделия (ярлыки,
// бирки, упаковка), которые физически идут на вещь.
//
// Компонент едет ПО НОМЕРУ СТИЛЯ, никогда по id (§5.2). Номер читается карточкой-компонентом и
// мемоизируется: одна и та же бирка на карточке встречается сколько угодно раз, а чтение карточки
// — не дешёвая операция.
func (s *Server) collectArchiveAssembly(ctx context.Context, card *entity.TechCard,
	sizes map[int]string, sc *archiveSidecars,
) ([]techcardarchive.AssemblyLink, []techcardarchive.ExportHole, error) {
	lines, err := s.repo.TechCards().ListStyleAssembly(ctx, card.Id)
	if err != nil {
		return nil, nil, fmt.Errorf("tech card archive: load style assembly: %w", err)
	}
	out := make([]techcardarchive.AssemblyLink, 0, len(lines))
	var holes []techcardarchive.ExportHole

	styleNumbers := make(map[int]string, len(lines))
	for _, a := range lines {
		number, resolved := styleNumbers[a.ComponentTechCardId]
		if !resolved {
			component, err := s.repo.TechCards().GetTechCardById(ctx, a.ComponentTechCardId)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				number = ""
			case err != nil:
				return nil, nil, fmt.Errorf("tech card archive: load assembly component %d: %w",
					a.ComponentTechCardId, err)
			case component == nil:
				number = ""
			default:
				number = strings.TrimSpace(component.StyleNumber.String)
			}
			styleNumbers[a.ComponentTechCardId] = number
		}
		if number == "" {
			// Обе беды — «карточки нет» и «у карточки нет номера стиля» — на выходе одно и то же:
			// строку нечем адресовать в чужой базе. Код один, различие живёт в detail.
			holes = append(holes, archiveHole(techcardarchive.EntityAssembly,
				fmt.Sprintf("component_tech_card_id=%d", a.ComponentTechCardId),
				techcardarchive.ReasonAssemblyComponentNotFound,
				"the component card is gone or carries no style number; the line is not exported"))
			continue
		}

		var sizeName *string
		if a.SizeId.Valid {
			name := strings.TrimSpace(a.SizeName.String)
			if name == "" {
				name = sizes[int(a.SizeId.Int32)]
			}
			if name == "" {
				// null здесь означало бы «строка на ВСЕ размеры» — то есть другое количество
				// ярлыков на прогон. Молча расширить строку нельзя, поэтому она не едет.
				holes = append(holes, archiveHole(techcardarchive.EntitySize, fmt.Sprintf("size_id=%d", a.SizeId.Int32),
					techcardarchive.ReasonSizeUnknown,
					"the assembly line's size is not in the dictionary; the line is not exported "+
						"because a null size would silently widen it to every size"))
				continue
			}
			sc.rememberSize(int(a.SizeId.Int32), name)
			sizeName = archiveStringPtr(name)
		}

		out = append(out, techcardarchive.AssemblyLink{
			ComponentStyleNumber: number,
			SizeName:             sizeName,
			Qty:                  a.Qty.String(),
			PrintNote:            a.PrintNote.String,
			PositionNote:         a.PositionNote.String,
			Active:               a.Active,
		})
	}
	return out, holes, nil
}

// ────────────────────────────── colorways.json ──────────────────────────────

// collectArchiveColorways собирает СПРАВОЧНУЮ полезную нагрузку колорвеев: колорвей — это продукт,
// а импорт продуктов не создаёт (§5.3). Файл едет, чтобы позднейшее явное действие «создать
// колорвеи из архива» имело из чего их построить, и чтобы человек мог прочитать, какими они были.
//
// ДЕНЕГ ЗДЕСЬ НЕТ И НЕ БЫЛО: ни cost_price колорвея, ни line_total/size_run_total строки рецепта
// не читаются вовсе. Это не «вычистили после», а «не попросили» — единственная форма, которую
// нельзя забыть повторить в следующей правке.
//
// Лаб-дипы не едут: это переписка с красильней, а не спецификация вещи.
func collectArchiveColorways(card *entity.TechCard, sizes map[int]string, sc *archiveSidecars,
) ([]techcardarchive.ColorwayPayload, []techcardarchive.ExportHole) {
	if len(card.Colorways) == 0 {
		return []techcardarchive.ColorwayPayload{}, nil
	}
	bomKeys := archiveBomLineKeys(card)
	pieceKeys := make(map[int64]string, len(card.Pieces))
	for i := range card.Pieces {
		pieceKeys[int64(card.Pieces[i].Id)] = card.Pieces[i].LineKey
	}

	out := make([]techcardarchive.ColorwayPayload, 0, len(card.Colorways))
	var holes []techcardarchive.ExportHole
	for i := range card.Colorways {
		cw := &card.Colorways[i]
		ref := fmt.Sprintf("color_code=%s", cw.ColorCode)

		recipe := make([]techcardarchive.RecipeLine, 0, len(cw.Usages))
		for j := range cw.Usages {
			u := &cw.Usages[j]
			line := techcardarchive.RecipeLine{
				BomLineKey:        bomKeys[u.BomItemId.Int64],
				PieceLineKey:      pieceKeys[u.PieceId.Int64],
				Placement:         u.Placement.String,
				Color:             u.Color.String,
				Pantone:           u.Pantone.String,
				Consumption:       decimalOrEmpty(u.Consumption),
				Quantity:          decimalOrEmpty(u.Quantity),
				MaterialRef:       u.MaterialId.Int64,
				ConsumptionSource: u.ConsumptionSource.String,
				WasteSelvedgePct:  decimalOrEmpty(u.WasteSelvedgePct),
				WasteCutPct:       decimalOrEmpty(u.WasteCutPct),
			}
			for _, cons := range u.SizeConsumptions {
				name, ok := sizes[cons.SizeId]
				if !ok {
					holes = append(holes, archiveHole(techcardarchive.EntityColorway, ref,
						techcardarchive.ReasonSizeUnknown,
						fmt.Sprintf("per-size consumption for size_id=%d has no name; the figure is not exported",
							cons.SizeId)))
					continue
				}
				sc.rememberSize(cons.SizeId, name)
				if line.SizeConsumptions == nil {
					line.SizeConsumptions = make(map[string]string, len(u.SizeConsumptions))
				}
				line.SizeConsumptions[name] = cons.Consumption.String()
			}
			recipe = append(recipe, line)
		}

		// Маппинг деталей висит на КОЛОРВЕЕ, хотя на проводе он живёт под деталью: в архиве
		// владелец связи — колорвей, потому что читатель спрашивает «чем кроился этот цвет».
		pieceMaterials := make([]techcardarchive.PieceMaterialLine, 0)
		for pi := range card.Pieces {
			p := &card.Pieces[pi]
			for mi := range p.Materials {
				m := &p.Materials[mi]
				if m.ColorwayID != cw.Id {
					continue
				}
				pieceMaterials = append(pieceMaterials, techcardarchive.PieceMaterialLine{
					PieceLineKey:     p.LineKey,
					BomLineKey:       m.BomLineKey,
					FusingBomLineKey: m.FusingBomLineKey,
					Note:             m.Note.String,
				})
			}
		}

		out = append(out, techcardarchive.ColorwayPayload{
			ColorCode:      cw.ColorCode,
			BaseSKU:        cw.BaseSku.String,
			Recipe:         recipe,
			PieceMaterials: pieceMaterials,
		})
	}
	return out, holes
}

// ────────────────────────────── materials/index.json ──────────────────────────────

// collectArchiveMaterials строит паспорта ровно тех артикулов каталога, на которые карта
// ссылается: умолчания слотов BOM и пины рецептов. БЕЗ цен и без истории цен — паспорт нужен,
// чтобы НАЙТИ тот же артикул в чужом каталоге, и ничего больше.
//
// Один ListMaterials на всё, а не GetMaterial на строку: у карточки десятки строк, и N круговых
// заходов за одной таблицей — это тот же дефект, что N+1 в списке.
func (s *Server) collectArchiveMaterials(ctx context.Context, card *entity.TechCard,
) ([]techcardarchive.MaterialPassport, []techcardarchive.ExportHole, error) {
	// Кто попросил артикул — первым выигрывает строка BOM: у неё есть line_key, а значит дыру
	// можно назвать так, чтобы оператор нашёл строку в card.json.
	wantedBy := make(map[int64]string)
	for i := range card.BomItems {
		b := &card.BomItems[i]
		if b.MaterialId.Valid && b.MaterialId.Int64 > 0 {
			if _, ok := wantedBy[b.MaterialId.Int64]; !ok {
				wantedBy[b.MaterialId.Int64] = fmt.Sprintf("bom_line_key=%s", b.LineKey)
			}
		}
	}
	bomKeys := archiveBomLineKeys(card)
	for i := range card.Colorways {
		for j := range card.Colorways[i].Usages {
			u := &card.Colorways[i].Usages[j]
			if !u.MaterialId.Valid || u.MaterialId.Int64 <= 0 {
				continue
			}
			if _, ok := wantedBy[u.MaterialId.Int64]; ok {
				continue
			}
			ref := fmt.Sprintf("material_id=%d", u.MaterialId.Int64)
			if key := bomKeys[u.BomItemId.Int64]; key != "" {
				ref = fmt.Sprintf("bom_line_key=%s", key)
			}
			wantedBy[u.MaterialId.Int64] = ref
		}
	}
	if len(wantedBy) == 0 {
		return []techcardarchive.MaterialPassport{}, nil, nil
	}

	// includeArchived: заархивированный артикул — это по-прежнему тот артикул, которым карта
	// нарисована, и его паспорт обязан уехать.
	mats, err := s.repo.TechCards().ListMaterials(ctx, "", true)
	if err != nil {
		return nil, nil, fmt.Errorf("tech card archive: load material catalogue: %w", err)
	}
	byID := make(map[int64]*entity.MaterialWithPrice, len(mats))
	for i := range mats {
		byID[int64(mats[i].Id)] = &mats[i]
	}

	ids := make([]int64, 0, len(wantedBy))
	for id := range wantedBy {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	out := make([]techcardarchive.MaterialPassport, 0, len(ids))
	var holes []techcardarchive.ExportHole
	for _, id := range ids {
		m, ok := byID[id]
		if !ok {
			// Строка BOM самодостаточна — у неё свои name/supplier/composition/unit (0068), — так
			// что карта уезжает целой, а дыра говорит, чего у неё больше нет в каталоге.
			holes = append(holes, archiveHole(techcardarchive.EntityMaterial, wantedBy[id],
				techcardarchive.ReasonMaterialNotFound,
				fmt.Sprintf("material_id=%d is not in the exporting catalogue; the line keeps its own name/supplier/unit", id)))
			continue
		}
		out = append(out, archiveMaterialPassport(id, m))
	}
	return out, holes, nil
}

// archiveMaterialPassport переводит артикул каталога в паспорт формата.
//
// Перечисления (unit_code, class) снимаются с dto-конвертера, а не выводятся здесь второй
// таблицей: словарь единиц живёт в entity и проецируется на провод РОВНО в одном месте, и вторая
// копия разошлась бы с ним молча. Из построенного сообщения читаются только два имени — цена в
// паспорт не попадает по построению типа, у него нет такого поля.
func archiveMaterialPassport(id int64, m *entity.MaterialWithPrice) techcardarchive.MaterialPassport {
	wire := dto.ConvertEntityMaterialToPb(*m)
	p := techcardarchive.MaterialPassport{
		Ref:                id,
		Code:               m.Code.String,
		Name:               m.Name,
		Supplier:           m.Supplier.String,
		SupplierRef:        m.SupplierRef.String,
		Composition:        m.Composition.String,
		Spec:               m.Spec.String,
		Unit:               m.Unit.String,
		UnitCode:           archiveKnownEnumName(wire.GetUnitCode().String(), "MATERIAL_UNIT_UNKNOWN"),
		Class:              archiveKnownEnumName(wire.GetMaterialClass().String(), "MATERIAL_CLASS_UNKNOWN"),
		Color:              m.Color.String,
		Pantone:            m.Pantone.String,
		CuttingCoefficient: decimalOrEmpty(m.CuttingCoefficient),
		FabricThicknessMm:  decimalOrEmpty(m.FabricThicknessMm),
		Notes:              m.Notes.String,
	}
	for _, c := range m.CompositionEntries {
		p.CompositionEntries = append(p.CompositionEntries, techcardarchive.CompositionEntry{
			FiberCode: c.FiberCode, Percent: c.Percent.String(),
		})
	}
	switch entity.MaterialClass(m.MaterialClass) {
	case entity.MaterialClassFabric:
		if a := m.FabricAttr; a != nil {
			p.Attributes = &techcardarchive.MaterialAttributes{Fabric: &techcardarchive.MaterialFabricAttrs{
				WidthCm:         decimalOrEmpty(a.WidthCm),
				WeightGsm:       decimalOrEmpty(a.WeightGsm),
				FabricDirection: a.FabricDirection.String,
				ShrinkagePct:    decimalOrEmpty(a.ShrinkagePct),
				RollLengthM:     decimalOrEmpty(a.RollLengthM),
				SelvedgeCm:      a.SelvedgeCm.String(),
			}}
		}
	case entity.MaterialClassHardware:
		if a := m.HardwareAttr; a != nil {
			p.Attributes = &techcardarchive.MaterialAttributes{Hardware: &techcardarchive.MaterialHardwareAttrs{
				DiameterMm:   decimalOrEmpty(a.DiameterMm),
				Dimensions:   a.Dimensions.String,
				Finish:       a.Finish.String,
				BaseMaterial: a.BaseMaterial.String,
				WeightG:      decimalOrEmpty(a.WeightG),
			}}
		}
	case entity.MaterialClassThread:
		if a := m.ThreadAttr; a != nil {
			p.Attributes = &techcardarchive.MaterialAttributes{Thread: &techcardarchive.MaterialThreadAttrs{
				TicketTex:      a.TicketTex.String,
				LengthPerConeM: decimalOrEmpty(a.LengthPerConeM),
				NeedleReco:     a.NeedleReco.String,
			}}
		}
	case entity.MaterialClassPackaging:
		if a := m.PackagingAttr; a != nil {
			p.Attributes = &techcardarchive.MaterialAttributes{Packaging: &techcardarchive.MaterialPackagingAttrs{
				Substrate:   a.Substrate.String,
				Dimensions:  a.Dimensions.String,
				Gsm:         decimalOrEmpty(a.Gsm),
				PrintMethod: a.PrintMethod.String,
			}}
		}
	case entity.MaterialClassOther:
		if len(m.OtherAttrs) > 0 {
			p.Attributes = &techcardarchive.MaterialAttributes{Other: string(m.OtherAttrs)}
		}
	}
	return p
}

// ────────────────────────────── media/ ──────────────────────────────

// archiveMediaSlot — медиа карты и то, чем его подписал ПЕРВЫЙ назвавший слот.
type archiveMediaSlot struct {
	id      int
	kind    string
	caption string
}

// collectArchiveMedia кладёт в архив БАЙТЫ каждого медиа карточки — ссылка на наш CDN выгрузкой не
// является. Одна запись НА МЕДИА, а не на слот: та же фотография в эскизе и в выноске — одна
// запись и один файл (FORMAT.md §5.5); связь «слот → медиа» живёт в card.json.
//
// Объект не читается — дыра media_object_missing, слот остаётся в card.json, и импорт отчитается о
// нём второй раз (media_missing). Это не дубль, а две разные новости: здесь байты не уехали, там —
// их нечем положить.
func (s *Server) collectArchiveMedia(ctx context.Context, card *entity.TechCard, sp *archiveSpool,
) ([]techcardarchive.MediaIndexEntry, []techcardarchive.ExportHole, error) {
	slots := archiveCardMediaSlots(card)
	if len(slots) == 0 {
		return []techcardarchive.MediaIndexEntry{}, nil, nil
	}
	ids := make([]int, 0, len(slots))
	for _, sl := range slots {
		ids = append(ids, sl.id)
	}
	rows, err := s.repo.Media().GetMediaByIds(ctx, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("tech card archive: load media rows: %w", err)
	}

	out := make([]techcardarchive.MediaIndexEntry, 0, len(slots))
	var holes []techcardarchive.ExportHole
	for _, sl := range slots {
		ref := fmt.Sprintf("media_id=%d", sl.id)
		row, ok := rows[sl.id]
		if !ok {
			holes = append(holes, archiveHole(techcardarchive.EntityMedia, ref, techcardarchive.ReasonMediaObjectMissing,
				"the media row is gone from the library; the slot travels without bytes"))
			continue
		}
		blob, err := s.archiveBlobFromURL(ctx, sp, techcardarchive.DirMedia, row.FullSizeMediaURL)
		if err != nil {
			if archiveIsFatal(err) {
				return nil, nil, err
			}
			holes = append(holes, archiveHole(techcardarchive.EntityMedia, ref, techcardarchive.ReasonMediaObjectMissing,
				fmt.Sprintf("full-size object: %v", err)))
			continue
		}
		out = append(out, techcardarchive.MediaIndexEntry{
			Ref:     int32(sl.id),
			File:    blob.Name,
			SHA256:  blob.SHA256,
			Kind:    sl.kind,
			Caption: sl.caption,
			Width:   int32(row.FullSizeWidth),
			Height:  int32(row.FullSizeHeight),
		})
	}
	return out, holes, nil
}

// archiveCardMediaSlots собирает множество media_id карточки в устойчивом порядке, подписывая
// каждое тем, чем его назвал ПЕРВЫЙ встретившийся слот.
//
// Носители перечислены по реестру использования медиа (internal/store/content/media_usage.go): у
// тех-карты их ровно четыре — tech_card_media, tech_card_callout, tech_card_detail_media,
// tech_card_operation_media. Пятого нет, и добавлять его сюда «на всякий случай» нечем.
//
// Подпись и вид берутся ТОЛЬКО у эскизных списков — так велит §5.5. У выноски, детали и шага в
// индексе пусто не потому, что подписи нет, а потому что индекс говорит «эти байты — это медиа», а
// не «зачем оно тут»: назначение живёт в card.json.
func archiveCardMediaSlots(card *entity.TechCard) []archiveMediaSlot {
	seen := make(map[int]bool)
	out := make([]archiveMediaSlot, 0, len(card.Media))
	add := func(id int, kind, caption string) {
		if id <= 0 || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, archiveMediaSlot{id: id, kind: kind, caption: caption})
	}
	for _, m := range card.Media {
		add(m.MediaId,
			archiveEnumName("TECH_CARD_MEDIA_KIND_", string(m.Kind), pb_common.TechCardMediaKind_value),
			m.Caption.String)
	}
	for _, c := range card.Callouts {
		if c.MediaId.Valid {
			add(int(c.MediaId.Int32), "", "")
		}
	}
	for _, d := range card.Details {
		for _, id := range d.MediaIds {
			add(id, "", "")
		}
	}
	for i := range card.Operations {
		for _, om := range card.Operations[i].Media {
			add(om.MediaId, "", "")
		}
	}
	return out
}

// ────────────────────────────── patterns/ ──────────────────────────────

// collectArchivePatterns кладёт в архив файлы выкроек и их индекс.
//
// size_name = null законно и обычно: лист, заведённый без размера, градуирован внутри самого DXF
// (миграция 0281). Именно поэтому нечитаемое имя размера здесь НЕ повод выбросить лист, в отличие
// от строки сборки: там null — это утверждение «на все размеры», а тут — «размер не проставлен», и
// потеря графы не портит геометрию. Лист едет, дыра называет потерю.
func (s *Server) collectArchivePatterns(ctx context.Context, card *entity.TechCard,
	sizes map[int]string, sc *archiveSidecars, sp *archiveSpool,
) ([]techcardarchive.PatternIndexEntry, []techcardarchive.ExportHole, error) {
	if len(card.Patterns) == 0 {
		return []techcardarchive.PatternIndexEntry{}, nil, nil
	}
	out := make([]techcardarchive.PatternIndexEntry, 0, len(card.Patterns))
	var holes []techcardarchive.ExportHole
	for i := range card.Patterns {
		p := &card.Patterns[i]
		ref := fmt.Sprintf("line_key=%s", p.LineKey)

		var sizeName *string
		if p.SizeId > 0 {
			if name, ok := sizes[p.SizeId]; ok {
				sc.rememberSize(p.SizeId, name)
				sizeName = archiveStringPtr(name)
			} else {
				holes = append(holes, archiveHole(techcardarchive.EntityPattern, ref, techcardarchive.ReasonSizeUnknown,
					fmt.Sprintf("the sheet is filed under size_id=%d, which has no name; the sheet travels without a size", p.SizeId)))
			}
		}

		blob, err := s.archiveBlobFromURL(ctx, sp, techcardarchive.DirPatterns, p.URL)
		if err != nil {
			if archiveIsFatal(err) {
				return nil, nil, err
			}
			holes = append(holes, archiveHole(techcardarchive.EntityPattern, ref, techcardarchive.ReasonPatternInvalid,
				fmt.Sprintf("the sheet's object could not be read: %v", err)))
			continue
		}

		out = append(out, techcardarchive.PatternIndexEntry{
			LineKey:       p.LineKey,
			File:          blob.Name,
			SHA256:        blob.SHA256,
			SizeName:      sizeName,
			Version:       int32(p.Version),
			Name:          p.Name.String,
			Filename:      p.Filename.String,
			FabricPurpose: archiveEnumName("TECH_CARD_BOM_PURPOSE_", p.FabricPurpose.String, pb_common.TechCardBomPurpose_value),
			BomLineKey:    p.BomLineKey.String,
		})
	}
	return out, holes, nil
}

// ────────────────────────────── markers/ ──────────────────────────────

// collectArchiveMarkers кладёт в архив раскладки КАРТОЧКИ — целиком, сырым protojson
// common.TechCardMarker: summary и layout вместе, геометрия внутри блоба, без ссылок наружу.
//
// РАСКЛАДКИ ПРОГОНА НЕ ЕДУТ. Они принадлежат прогону (run_id, 0282), а прогон в архив стиля не
// входит: карточное чтение их уже отфильтровало, и проверка ниже — вторая половина того же
// утверждения, чтобы смена чтения не протащила их молча.
//
// source_url у деталей раскладки гасится: это ссылка на CDN экспортирующего инстанса, а FORMAT.md
// §4 запрещает возить наши ссылки — контуры лежат внутри блоба, ссылка указывает на хост, которого
// у принимающей стороны нет. Остальные чужие идентичности внутри блоба (summary.id,
// summary.tech_card_id, summary.colorway_id, все size_id) едут ВЕРБАТИМ: их разбирает импорт по
// §5.7, и переписать их здесь значило бы отдать импорту блоб, про который уже нельзя сказать, чем
// он был.
func (s *Server) collectArchiveMarkers(ctx context.Context, card *entity.TechCard,
	sizes map[int]string, sc *archiveSidecars,
) ([]techcardarchive.MarkerIndexEntry, []archiveJSONFile, []techcardarchive.ExportHole, error) {
	if len(card.Markers) == 0 {
		return []techcardarchive.MarkerIndexEntry{}, nil, nil, nil
	}
	bomKeys := archiveBomLineKeys(card)
	index := make([]techcardarchive.MarkerIndexEntry, 0, len(card.Markers))
	files := make([]archiveJSONFile, 0, len(card.Markers))
	var holes []techcardarchive.ExportHole
	used := make(map[string]bool, len(card.Markers))
	counters := make(map[string]int, len(card.Markers))

	for i := range card.Markers {
		summary := card.Markers[i]
		if summary.RunId.Valid && summary.RunId.Int64 > 0 {
			continue
		}
		stored, err := s.repo.TechCards().GetMarker(ctx, summary.Id)
		switch {
		case errors.Is(err, entity.ErrMarkerNotFound), errors.Is(err, sql.ErrNoRows):
			// Раскладку удалили между чтением карточки и чтением её геометрии. Закрытый словарь
			// причин не содержит кода для этой гонки, а изобретать код на месте вызова запрещено
			// (reasons.go): остаётся громкий лог и пропуск.
			slog.Default().WarnContext(ctx, "tech card archive: marker vanished mid-export",
				slog.Int("tech_card_id", card.Id), slog.Int("marker_id", summary.Id))
			continue
		case err != nil:
			return nil, nil, nil, fmt.Errorf("tech card archive: load marker %d: %w", summary.Id, err)
		case stored == nil:
			continue
		}

		wire := &pb_common.TechCardMarker{Summary: dto.TechCardMarkerSummaryToPb(stored.TechCardMarkerSummary)}
		var layout pb_common.TechCardMarkerLayout
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(stored.Layout), &layout); err != nil {
			// Ровно то же вырождение, что у GetTechCardMarker: числа summary остаются истиной, и
			// терять раскладку целиком из-за нечитаемой геометрии хуже, чем довезти её цифры с
			// предупреждением внутри самого блоба.
			slog.Default().ErrorContext(ctx, "tech card archive: stored marker layout does not parse; exporting summary only",
				slog.Int("marker_id", stored.Id), slog.String("err", err.Error()))
			wire.Layout = &pb_common.TechCardMarkerLayout{
				Warnings: []string{"the stored marker could not be read at export time — only the summary figures travel"},
			}
		} else {
			wire.Layout = &layout
		}
		for _, piece := range wire.GetLayout().GetPieces() {
			piece.SourceUrl = ""
		}

		blob, err := protojson.Marshal(wire)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("tech card archive: encode marker %d: %w", stored.Id, err)
		}

		var sizeName *string
		if composition := stored.CompositionOrLegacy(); len(composition) == 1 {
			if name, ok := sizes[composition[0].SizeId]; ok {
				sc.rememberSize(composition[0].SizeId, name)
				sizeName = archiveStringPtr(name)
			} else {
				holes = append(holes, archiveHole(techcardarchive.EntityMarker, fmt.Sprintf("marker_name=%s", stored.Name),
					techcardarchive.ReasonSizeUnknown,
					fmt.Sprintf("the marker's size_id=%d has no name; the index cannot label it", composition[0].SizeId)))
			}
		} else {
			// Ноль записей состава или несколько — единственного размера у раскладки нет; состав
			// живёт внутри блоба, и индекс не притворяется, что знает больше.
			for _, entry := range composition {
				if name, ok := sizes[entry.SizeId]; ok {
					sc.rememberSize(entry.SizeId, name)
				}
			}
		}

		name := archiveMarkerFileName(sizeName, counters, used)
		files = append(files, archiveJSONFile{Name: name, Data: blob})
		key := stored.BomLineKey.String
		if key == "" {
			key = bomKeys[stored.BomItemId.Int64]
		}
		index = append(index, techcardarchive.MarkerIndexEntry{
			File:       name,
			SizeName:   sizeName,
			MarkerName: stored.Name,
			BomLineKey: key,
		})
	}
	return index, files, holes, nil
}

// archiveMarkerFileName строит markers/<slug>-<n>.json: slug — имя размера в нижнем регистре, где
// всё вне [a-z0-9] заменено дефисом, либо литерал "mixed" у раскладки без единственного размера;
// n — счётчик с единицы, делающий имя уникальным внутри архива.
//
// Имя — САХАР ДЛЯ ГЛАЗА. Читатель обязан находить раскладку через markers/index.json и никогда
// разбором имени файла: два размера с именами "M " и "m" дают один slug, и только индекс говорит,
// который из них где.
func archiveMarkerFileName(sizeName *string, counters map[string]int, used map[string]bool) string {
	slug := "mixed"
	if sizeName != nil {
		var b strings.Builder
		for _, r := range strings.ToLower(*sizeName) {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				b.WriteRune(r)
			default:
				b.WriteByte('-')
			}
		}
		if s := strings.Trim(b.String(), "-"); s != "" {
			slug = s
		}
	}
	for {
		counters[slug]++
		name := fmt.Sprintf("%s%s-%d.json", techcardarchive.DirMarkers, slug, counters[slug])
		if !used[name] {
			used[name] = true
			return name
		}
	}
}

// ────────────────────────────── общее ──────────────────────────────

// archiveBlobFromURL превращает СОХРАНЁННЫЙ url объекта в отложенный на диск файл архива.
//
// Ключ берётся из строки БД и только из неё; сегментный гард стоит в бакете (GetManagedObject
// отказывает на ключе вне разрешённых папок ДО обращения к S3), поэтому здесь остаётся разбор
// пути — ровно то, что делает приватная bucket.objectKeyFromURL.
func (s *Server) archiveBlobFromURL(ctx context.Context, sp *archiveSpool, prefix, rawURL string) (archiveBlob, error) {
	key, err := archiveObjectKeyFromURL(rawURL)
	if err != nil {
		return archiveBlob{}, err
	}
	rc, _, err := s.bucket.GetManagedObject(ctx, key)
	if err != nil {
		return archiveBlob{}, fmt.Errorf("object %q: %w", key, err)
	}
	defer rc.Close()
	blob, err := sp.add(prefix, archiveObjectExt(key), rc)
	if err != nil {
		return archiveBlob{}, err
	}
	return blob, nil
}

// archiveObjectExt — расширение из ключа объекта, приведённое к тому, что можно поставить в имя
// записи zip.
//
// Санитайзер, а не просто path.Ext: ключ приходит из url, а u.Path раскодирован, так что «%5C» в
// сохранённой ссылке доехало бы до имени записи обратным слэшем — именно тем символом, который
// FORMAT.md §1.1 в имени записи запрещает. Не белый список: .jpeg и .webp законны ровно так же,
// как перечисленные в спеке, и отбрасывать их было бы потерей типа файла на пустом месте.
func archiveObjectExt(key string) string {
	ext := strings.ToLower(path.Ext(key))
	if ext == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range ext {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.':
			b.WriteRune(r)
		}
		if b.Len() >= 16 {
			break
		}
	}
	out := b.String()
	if out == "." || strings.Contains(out, "..") {
		return ""
	}
	return out
}

// archiveObjectKeyFromURL достаёт ключ объекта из сохранённого https-url.
func archiveObjectKeyFromURL(rawURL string) (string, error) {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return "", errors.New("the row carries no object url")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse object url %q: %w", raw, err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("object url %q is not a managed https url", raw)
	}
	key := strings.Trim(u.Path, "/")
	if key == "" {
		return "", fmt.Errorf("object url %q carries no key", raw)
	}
	return key, nil
}

// archiveIsFatal отделяет отказ, который обязан похоронить весь экспорт, от битой ссылки, которая
// становится дырой. Один предикат на все сайдкары: два места, решающих это по-разному, — и одно из
// них рано или поздно проглотит отказ диска как «данных нет».
func archiveIsFatal(err error) bool {
	return errors.Is(err, errArchiveSpoolFailed) || errors.Is(err, errArchiveContentTooLarge)
}

// archiveHole собирает одну дыру. Причина — из закрытого словаря reasons.go, detail — свободный
// текст без контракта.
func archiveHole(entityName, ref string, reason techcardarchive.Reason, detail string) techcardarchive.ExportHole {
	return techcardarchive.ExportHole{Entity: entityName, Ref: ref, Reason: reason, Detail: detail}
}

// archiveBomLineKeys — id строки BOM → её стабильный line_key. Строится один раз на карточку:
// связку «bom_item_id → line_key» спрашивают и рецепты, и паспорта, и раскладки.
func archiveBomLineKeys(card *entity.TechCard) map[int64]string {
	out := make(map[int64]string, len(card.BomItems))
	for i := range card.BomItems {
		out[int64(card.BomItems[i].Id)] = card.BomItems[i].LineKey
	}
	return out
}

// archiveEnumName собирает имя значения proto-перечисления по хранимому слову словаря и ПРОВЕРЯЕТ
// его по сгенерированной таблице имён. Проверка, а не второе объявление словаря: авторитет —
// сгенерированный код, здесь только поиск в нём. "" — хранимое слово не называет ни одного
// значения, и тогда в архив не едет ничего: выдуманное имя перечисления хуже пустого поля.
func archiveEnumName(prefix, stored string, values map[string]int32) string {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return ""
	}
	name := prefix + strings.ToUpper(stored)
	if _, ok := values[name]; !ok {
		return ""
	}
	return name
}

// archiveKnownEnumName пропускает имя значения перечисления, кроме UNKNOWN-значения: «сервер не
// знает такой единицы» — это не факт об артикуле, а отсутствие факта, и рядом уже едет исходная
// свободная строка, которая для такой строки и есть вся правда.
func archiveKnownEnumName(name, unknown string) string {
	if name == unknown {
		return ""
	}
	return name
}

// archiveStringPtr — указатель на копию строки: поля *string в индексах различают «значения нет»
// (null) и «значение пустое», и брать адрес переменной цикла для этого нельзя.
func archiveStringPtr(s string) *string {
	v := s
	return &v
}

// rememberSize запоминает пару id→имя для manifest.id_maps.sizes.
func (a *archiveSidecars) rememberSize(id int, name string) {
	if id <= 0 || name == "" {
		return
	}
	a.SizeNames[id] = name
}
