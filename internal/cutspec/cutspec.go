// Package cutspec выбирает СПЕЦИФИКАЦИЮ, по которой печатается наряд на партию: снапшот релиза,
// когда прогон к релизу привязан, иначе живую карточку — и говорит, чем при этом пришлось
// пожертвовать.
//
// ПАКЕТ ЗАВЕДЁН ПОТОМУ, ЧТО ЧИТАТЕЛЕЙ ДВА, А ПРАВИЛО ОДНО. Правило жило внутри админского хендлера
// кат-плана; публичный наряд (internal/runpackaccess) писался в другой ветке, где этой функции ещё
// не существовало, и считал кат-лист ПО ЖИВОЙ КАРТЕ, ставя в шапку номер релиза. Расхождение двух
// экземпляров одного правила стоило ровно того, ради чего релизы и заводят: швея читала «Rev.3» и
// кроила по карточке, которая уехала после релиза. Поэтому экземпляр остаётся один, а читатели его
// зовут; третий читатель обязан прийти сюда же, а не списать логику в третий раз.
//
// ДЕНЬГИ. Замороженный блоб релиза несёт костинг и цены BOM, и распарсить его, не подняв их в
// память, нельзя. Наружу из пакета уезжает только то, что собрал dto.CutSpecCardFromReleaseSnapshot
// (градация, детали, BOM-связи, рецепты, aux-цвета) плюс мета релиза, собранная здесь ПОИМЁННО без
// unit_cost/currency. Это важно ровно потому, что один из читателей — публичный эндпоинт наряда, где
// нет аккаунта, под который можно было бы срезать костинг постфактум.
//
// ИСКЛЮЧЕНИЕ РОВНО ОДНО И ОНО НАЗВАНО: FrozenColorwayCosts — дверь для плановой цены релизного
// прогона. Она отдаёт ЗАМОРОЖЕННЫЕ ЦЕНЫ КОЛОРВЕЕВ и больше ничего: ни блоба, ни карточки, ни
// строк BOM. Дверь заведена здесь, а не рядом с деньгами, потому что «какой релиз, наш ли он, и
// читается ли он» — правило ЭТОГО пакета, и второй его копии быть не должно (см. заголовок выше).
// Публичный наряд её не зовёт и звать не имеет права: у него нет аккаунта, под которым деньги
// разрешены.
package cutspec

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/encoding/protojson"
)

// Cards — узкий срез dependency.TechCards, нужный выбору спецификации. Узкий он затем, чтобы у
// публичного наряда осталось право собирать тест на фейке в двадцать строк, а не на моке всего
// репозитория карточек.
type Cards interface {
	GetTechCardById(ctx context.Context, id int) (*entity.TechCard, error)
	GetTechCardRelease(ctx context.Context, id int) (*entity.TechCardRelease, error)
	GetMaterial(ctx context.Context, id int) (*entity.MaterialWithPrice, error)
}

// Source — ПО ЧЕМУ посчитана спецификация. Строкой, а не булевым флагом: «посчитано по живой карте»
// имеет две совершенно разные причины, и цеху они означают разное. У прогона без релиза живая
// карточка — это норма (кроить больше не по чему), а у прогона с релизом — авария: утверждённая
// спецификация существует, но прочитать её не удалось. Один bool слил бы норму с аварией.
type Source string

const (
	// SourceReleaseSnapshot — посчитано по замороженному снапшоту релиза. Нормальный путь для
	// прогона с релизом.
	SourceReleaseSnapshot Source = "release_snapshot"
	// SourceLiveCard — прогон не привязан к релизу, считать больше не по чему.
	SourceLiveCard Source = "live_card"
	// SourceLiveCardFallback — прогон привязан к релизу, но снапшот не прочитан (нет строки, чужая
	// карточка, блоб не парсится, блоб пуст). Спецификация МОГЛА измениться после релиза, и читатель
	// обязан это показать, а не молча выдать номер ревизии над чужими числами.
	SourceLiveCardFallback Source = "live_card_fallback"
)

// Spec — выбранная спецификация и всё, что о выборе надо знать читателю.
type Spec struct {
	// Card — карточка, ПО КОТОРОЙ СЧИТАТЬ. Никогда nil при err == nil.
	Card *entity.TechCard
	// Release — мета релиза, ПО КОТОРОМУ ПОСЧИТАНО, и nil, когда посчитано по живой карточке — даже
	// если сам прогон на релиз ссылается. Поле называет спецификацию, а не ссылку прогона: соврать
	// в нём означало бы подписать чужой ревизией то, чего никто не утверждал.
	Release *entity.TechCardReleaseMeta
	Source  Source
	// Caveats — оговорки о ВЫБОРЕ спецификации, человеческими фразами. Пустые при нормальном пути.
	Caveats []string
}

// Resolve выбирает спецификацию прогона.
//
// РЕЛИЗ ПОБЕЖДАЕТ ЖИВУЮ КАРТОЧКУ, когда прогон к нему привязан, и это не вкус: фабрика кроит по
// замороженной спецификации, а живая карточка меняется каждый день. Напечатать наряд по живой
// карточке для прогона с релизом — значит выдать в цех крой, которого никто не утверждал.
//
// НО НЕЧИТАЕМЫЙ СНАПШОТ — НЕ ОТКАЗ. Блоб заморожен вместе со схемой, которая с тех пор уехала;
// правило hero-v2 (и GetTechCardRelease рядом) говорит деградировать, а не падать. Наряд по живой
// карточке с громкой оговоркой лучше, чем пустой экран у закройщика — при условии, что Source и
// Release честно называют, по чему посчитано.
//
// СБОЙ ЧТЕНИЯ — НАОБОРОТ, ОТКАЗ (ошибка наружу). Таймаут MySQL проходит и возвращается, а наряд,
// выданный в цех по неутверждённой спецификации, — нет. Деградация допустима только там, где
// снапшот сломан НАВСЕГДА, иначе одна сетевая икота печатает не ту раскладку.
func Resolve(ctx context.Context, cards Cards, run *entity.ProductionRun) (*Spec, error) {
	if run == nil {
		return nil, errors.New("cut spec: run is required")
	}
	spec := &Spec{Source: SourceLiveCard}

	if run.ReleaseId.Valid && run.ReleaseId.Int64 > 0 {
		card, meta, caveats, err := releaseSpec(ctx, cards, run)
		if err != nil {
			return nil, err
		}
		spec.Caveats = caveats
		if card != nil {
			spec.Card, spec.Release, spec.Source = card, meta, SourceReleaseSnapshot
			return spec, nil
		}
		spec.Source = SourceLiveCardFallback
	}

	card, err := cards.GetTechCardById(ctx, run.TechCardId)
	if err != nil {
		// sql.ErrNoRows едет ЗАВЁРНУТЫМ, но узнаваемым через errors.Is: у одного читателя это
		// NotFound, у другого — тот же голый 404, и оба отличают «карточки нет» от сбоя чтения.
		return nil, fmt.Errorf("cut spec: load tech card %d of run %d: %w", run.TechCardId, run.Id, err)
	}
	spec.Card = card
	return spec, nil
}

// releaseSpec пытается собрать спецификацию из снапшота релиза. Карточка nil без ошибки = «снапшот
// непригоден, деградируйте на живую карточку», и причина уже лежит в caveats.
func releaseSpec(ctx context.Context, cards Cards, run *entity.ProductionRun) (*entity.TechCard, *entity.TechCardReleaseMeta, []string, error) {
	relID := int(run.ReleaseId.Int64)
	rel, err := cards.GetTechCardRelease(ctx, relID)
	switch {
	// `err == nil && rel == nil` лежит в одной ветке с ErrNoRows намеренно: это то же «релиза нет»,
	// и без него оно провалилось бы ниже, где первым же действием разыменовывается.
	case errors.Is(err, sql.ErrNoRows), err == nil && rel == nil:
		return nil, nil, []string{fmt.Sprintf(
			"релиз #%d, на который ссылается прогон, не найден — наряд посчитан по ЖИВОЙ карточке", relID)}, nil
	case err != nil:
		return nil, nil, nil, fmt.Errorf("cut spec: load release %d of run %d: %w", relID, run.Id, err)
	}

	snap, caveat := parseReleaseSnapshot(ctx, rel, run.TechCardId, run.Id)
	if snap == nil {
		return nil, nil, []string{caveat}, nil
	}
	card := dto.CutSpecCardFromReleaseSnapshot(snap)
	if card == nil {
		return nil, nil, []string{fmt.Sprintf(
			"снапшот релиза Rev.%d пуст — наряд посчитан по ЖИВОЙ карточке, а она могла измениться после релиза",
			rel.ReleaseNumber)}, nil
	}
	resolveArticles(ctx, cards, card)

	// Мета собирается ПОИМЁННО, а не копированием rel.TechCardReleaseMeta: у релиза есть unit_cost,
	// currency и released_by, а читателей у этой меты двое, и один из них — публичный наряд без
	// RBAC. Что не скопировано, то невозможно случайно напечатать; проекции кат-листа нужны только
	// id и номер ревизии.
	meta := &entity.TechCardReleaseMeta{
		Id:            rel.Id,
		TechCardId:    rel.TechCardId,
		ReleaseNumber: rel.ReleaseNumber,
		CreatedAt:     rel.CreatedAt,
	}
	return card, meta, nil, nil
}

// parseReleaseSnapshot проверяет, что релиз принадлежит ЭТОЙ карточке, и разбирает его блоб.
// Читателей у этого правила двое — наряд и плановая цена релизного прогона, — а копия одна:
// разойдись они, один из двух печатал бы Rev.N над числами чужой ревизии, что и есть авария,
// ради предотвращения которой заведён весь пакет.
//
// snap == nil без ошибки = «снапшот непригоден, дальше по своему правилу», а caveat — человеческая
// причина ДЛЯ НАРЯДА (денежный читатель её выбрасывает: у него нет бумаги, на которую её печатать).
// runID = 0, когда прогона ещё нет: цену партии считают и на создании, до первичного ключа.
func parseReleaseSnapshot(ctx context.Context, rel *entity.TechCardRelease, techCardID, runID int) (*pb_common.TechCard, string) {
	if rel == nil {
		return nil, ""
	}
	// Имена полей те же, что были у этой записи в логе до вынесения функции: по ним ищут, и
	// переименование ради «теперь читателей двое» стоило бы ровно того, ради чего логи и пишут.
	attrs := []any{
		slog.Int("release_id", rel.Id),
		slog.Int("release_tech_card_id", rel.TechCardId),
		slog.Int("run_tech_card_id", techCardID),
	}
	if runID > 0 {
		attrs = append(attrs, slog.Int("run_id", runID))
	}

	// РЕЛИЗ ОБЯЗАН БЫТЬ РЕЛИЗОМ ЭТОЙ КАРТОЧКИ. В схеме это две независимые ссылки
	// (production_run.release_id и production_run.tech_card_id), составного ключа между ними нет, и
	// никто по дороге их не сверяет — снапшот-костинг прогона проверяет только, что релиз
	// существует. Не проверить здесь значит уметь напечатать цеху наряд по ЧУЖОЙ карточке (детали,
	// ткани и артикулы другого стиля под номером этой партии) и снять с партии цену чужого стиля.
	if rel.TechCardId != techCardID {
		slog.Default().WarnContext(ctx, "cut spec: run points at a release of another tech card", attrs...)
		return nil, fmt.Sprintf(
			"релиз #%d принадлежит другой карточке — наряд посчитан по ЖИВОЙ карточке этого прогона", rel.Id)
	}

	var snap pb_common.TechCard
	if uerr := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(rel.Snapshot), &snap); uerr != nil {
		// Текст ошибки цитирует поле замороженного блоба (а тот несёт костинг и цены BOM) — в
		// оговорку, которая уедет на бумагу и в публичный манифест, он не попадает.
		slog.Default().WarnContext(ctx, "cut spec: release snapshot won't parse",
			append(attrs, slog.String("err", uerr.Error()))...)
		return nil, fmt.Sprintf(
			"снапшот релиза Rev.%d не читается текущей схемой — наряд посчитан по ЖИВОЙ карточке, а она могла измениться после релиза",
			rel.ReleaseNumber)
	}
	return &snap, ""
}

// FrozenColorwayCosts — ДЕНЕЖНАЯ ДВЕРЬ пакета (единственная, см. заголовок): замороженные цены
// колорвеев релиза для плановой цены релизного прогона.
//
// Релиз сюда приезжает УЖЕ ПРОЧИТАННЫМ, а не по id: вызывающий держит его в руках ради
// замороженного скаляра, к которому обязан вернуться при любой непригодности снапшота, и второй
// запрос той же строки был бы платой ни за что.
//
// nil = «замороженных цен нет» (релиза нет, релиз чужой карточки, блоб не читается, денег в блобе
// нет). Деградации на живую карточку здесь НЕ БЫВАЕТ ни в каком виде — в отличие от Resolve, где
// наряд по живой карточке с громкой оговоркой лучше пустого экрана у закройщика. Цена так не
// умеет: посчитанная по живой карточке, она молча поедет от правок, сделанных ПОСЛЕ релиза, и
// заявит про партию число, которого никто не утверждал.
func FrozenColorwayCosts(ctx context.Context, rel *entity.TechCardRelease, techCardID int) *dto.ReleaseFrozenCosts {
	snap, _ := parseReleaseSnapshot(ctx, rel, techCardID, 0)
	if snap == nil {
		return nil
	}
	return dto.ReleaseFrozenColorwayCosts(snap)
}

// resolveArticles возвращает карточке из снапшота каталожные имена артикулов.
//
// Живое чтение приносит их само (TechCard.LinkedMaterials), а замороженный блок их не несёт — и без
// них наряд напечатал бы в колонке артикула ИМЯ РОЛИ слота: «основная ткань» вместо названия ткани.
// У колорвея с пином это уже не бедность, а ложь — пин и умолчание слота получили бы под разными
// material_id одну и ту же подпись, то есть исчезла бы ровно та разница, ради которой закройщик и
// смотрит в наряд.
//
// Множество то же, что у материального плана: умолчания слотов ПЛЮС пины рецептов. Промах чтения
// деградирует к снимку строки BOM и не валит запрос — наряд без каталожного имени всё ещё наряд.
func resolveArticles(ctx context.Context, cards Cards, card *entity.TechCard) {
	seen := make(map[int]bool, len(card.BomItems))
	ids := make([]int, 0, len(card.BomItems))
	add := func(id int) {
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for i := range card.BomItems {
		if card.BomItems[i].MaterialId.Valid {
			add(int(card.BomItems[i].MaterialId.Int64))
		}
	}
	for i := range card.Colorways {
		for j := range card.Colorways[i].Usages {
			if u := &card.Colorways[i].Usages[j]; u.MaterialId.Valid {
				add(int(u.MaterialId.Int64))
			}
		}
	}
	if len(ids) == 0 {
		return
	}
	linked := make(map[int]entity.MaterialWithPrice, len(ids))
	for _, mid := range ids {
		m, err := cards.GetMaterial(ctx, mid)
		if err != nil || m == nil {
			continue
		}
		linked[mid] = *m
	}
	card.LinkedMaterials = linked
}
