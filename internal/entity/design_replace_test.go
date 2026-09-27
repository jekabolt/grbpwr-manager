package entity

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ПРОБЫ ПРАВИЛА «ПЕРЕЗАПИСАТЬ» (0369, O-53) — решения без базы.
//
// Чтения (кадр, кадры его карточки) делает стор в транзакции флэттена, и они проверяются живыми
// пробами internal/store/design/replace_db_test.go (одноразовый контейнер, CI=1). Здесь — то, что от
// базы не зависит: какой отказ звучит при каком состоянии, В КАКОМ ПОРЯДКЕ, и стоит ли кусок листа.

const (
	replaceProbeCard  = 41
	replaceProbeMedia = 900
)

// replaceProbeOriginal — кадр, который ГОДИТСЯ: на этой карточке, медиа = подложка слоя, не заменён.
func replaceProbeOriginal() DesignPicture {
	return DesignPicture{Id: 7, TechCardId: replaceProbeCard, MediaId: replaceProbeMedia, Kind: DesignPictureKindFlat}
}

func replaceProbeBase(media int32) sql.NullInt32 { return sql.NullInt32{Int32: media, Valid: true} }

// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: без него каждая проба ниже зеленела бы и на правиле, отказывающем ВСЕГДА.
//
// Сюда же — кропы, которые в счёт не идут: вызывающий считает только стоящие (DesignStandingPieces),
// и ноль — это «ни один кусок не стоит», а не «не спросили»; и кадр НЕ на листе — нулевые факты
// значат «ничего не держит».
//
// Спрятанного оригинала здесь больше нет (27.09): он — отказ hidden_picture, и его пробы — ниже.
func TestDesignReplaceRefusalLetsTheNamedOriginalThrough(t *testing.T) {
	require.NoError(t, DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), replaceProbeOriginal(),
		DesignReplaceFacts{}))

	// Кроп и правка кропа — законные оригиналы: «edit a piece instead» ведёт именно сюда.
	piece := replaceProbeOriginal()
	piece.DerivedFrom = sql.NullInt32{Int32: 3, Valid: true}
	piece.Derivation = DesignDerivationCrop
	require.NoError(t, DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), piece, DesignReplaceFacts{}))
}

// КАЖДЫЙ ОТКАЗ НАЗЫВАЕТ СВОЁ, И ТОЛЬКО СВОЁ.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: снять любую из семи проверок (подслучай становится nil) — в том числе
// лист (27.09: «кадр на техническом листе» проходит, и тех-пакет печатает две плиты) и спрятанность
// (27.09: «оригинал спрятан» проходит, и правка встаёт преемником убранного кадра); перепутать
// сентинел (errors.Is подслучая краснеет); сравнивать медиа с чем-то кроме подложки слоя (случай
// «другой файл» проходит); читать заменённость по знаку вместо NULL-ности (случай replaced_by = 0,
// Valid — невозможный для писателя, но возможный для руки в базе — проходит мимо сторожа, которого
// UPDATE … WHERE replaced_by IS NULL всё равно не пустил бы: два сторожа одного факта разошлись бы).
func TestDesignReplaceRefusalNamesEachRefusal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		card  int
		base  sql.NullInt32
		pic   func(DesignPicture) DesignPicture
		facts DesignReplaceFacts
		want  error
	}{
		{"кадр чужой карточки", replaceProbeCard + 1, replaceProbeBase(replaceProbeMedia),
			func(p DesignPicture) DesignPicture { return p }, DesignReplaceFacts{}, ErrDesignReplaceMismatch},
		{"слой нарисован с чистого листа", replaceProbeCard, sql.NullInt32{},
			func(p DesignPicture) DesignPicture { return p }, DesignReplaceFacts{}, ErrDesignReplaceMismatch},
		{"подложка слоя — ноль", replaceProbeCard, sql.NullInt32{Valid: true},
			func(p DesignPicture) DesignPicture { return p }, DesignReplaceFacts{}, ErrDesignReplaceMismatch},
		{"слой нарисован поверх другого файла", replaceProbeCard, replaceProbeBase(replaceProbeMedia + 1),
			func(p DesignPicture) DesignPicture { return p }, DesignReplaceFacts{}, ErrDesignReplaceMismatch},
		{"кадр уже заменён", replaceProbeCard, replaceProbeBase(replaceProbeMedia),
			func(p DesignPicture) DesignPicture {
				p.ReplacedBy = sql.NullInt32{Int32: 12, Valid: true}
				return p
			}, DesignReplaceFacts{}, ErrDesignAlreadyReplaced},
		{"заменённость читается по NULL, а не по знаку", replaceProbeCard, replaceProbeBase(replaceProbeMedia),
			func(p DesignPicture) DesignPicture {
				p.ReplacedBy = sql.NullInt32{Valid: true}
				return p
			}, DesignReplaceFacts{}, ErrDesignAlreadyReplaced},
		{"оригинал спрятан", replaceProbeCard, replaceProbeBase(replaceProbeMedia),
			func(p DesignPicture) DesignPicture {
				p.HiddenAt = sql.NullTime{Time: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC), Valid: true}
				return p
			}, DesignReplaceFacts{}, ErrDesignHiddenPicture},
		{"кадр на техническом листе", replaceProbeCard, replaceProbeBase(replaceProbeMedia),
			func(p DesignPicture) DesignPicture { return p }, DesignReplaceFacts{OnTechnicalSheet: true}, ErrDesignTechnicalSheet},
		{"лист разрезан", replaceProbeCard, replaceProbeBase(replaceProbeMedia),
			func(p DesignPicture) DesignPicture { return p }, DesignReplaceFacts{StandingPieces: 2}, ErrDesignCutSheet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := DesignReplaceRefusal(tc.card, tc.base, tc.pic(replaceProbeOriginal()), tc.facts)
			require.Error(t, err)
			require.ErrorIs(t, err, tc.want)
			for _, other := range []error{ErrDesignReplaceMismatch, ErrDesignAlreadyReplaced, ErrDesignHiddenPicture,
				ErrDesignTechnicalSheet, ErrDesignCutSheet, ErrDesignHiddenPlate} {
				if !errors.Is(tc.want, other) {
					require.NotErrorIs(t, err, other, "один отказ — одна причина на проводе")
				}
			}
		})
	}
}

// ОТКАЗ ЛИСТА ГОВОРИТ, ЧТО СЛУЧИТСЯ И ЧТО ДЕЛАТЬ, — ТЕМ ЖЕ ГОЛОСОМ, ЧТО cut_sheet.
//
// Клиент показывает человеку слова сервера как есть (layerRefusalText), поэтому починка — в самом
// отказе: снять кадр с листа или сохранить правку новой картинкой.
func TestDesignReplaceRefusalTechnicalSheetSaysWhatToDo(t *testing.T) {
	err := DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), replaceProbeOriginal(),
		DesignReplaceFacts{OnTechnicalSheet: true})
	require.EqualError(t, err, "design: technical_sheet: the original, picture 7, is on the card's technical sheet and "+
		"would stay there beside the edit — take it off the sheet first, or save the edit as a new picture")
}

// ОТКАЗ СПРЯТАННОМУ ОРИГИНАЛУ ГОВОРИТ, ЧТО ДЕЛАТЬ, — ТЕМ ЖЕ ГОЛОСОМ, ЧТО ЛИСТ (27.09).
//
// Клиент показывает слова сервера как есть, поэтому починка — в самом отказе: сохранить правку новой
// картинкой. Сентинел — тот же, что у разреза спрятанного кадра, и на проводе одно слово
// hidden_picture; hidden_plate — отказ постановке в слот, клиенту другая новость.
//
// МУТАЦИИ: отдать hidden_plate (краснеет NotErrorIs); потерять починку или номер кадра (EqualError).
func TestDesignReplaceRefusalHiddenSaysWhatToDo(t *testing.T) {
	hidden := replaceProbeOriginal()
	hidden.HiddenAt = sql.NullTime{Time: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC), Valid: true}
	err := DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), hidden, DesignReplaceFacts{})
	require.ErrorIs(t, err, ErrDesignHiddenPicture)
	require.NotErrorIs(t, err, ErrDesignHiddenPlate)
	require.EqualError(t, err, "design: hidden_picture: the original, picture 7, is hidden — an edit cannot take "+
		"a hidden picture's place; save the edit as a new picture")
}

// ПОРЯДОК ОТКАЗОВ: от «чинится запросом» к «чинится другим жестом».
//
// Кадр, у которого неверно ВСЁ сразу, обязан получить replace_mismatch: запрос, назвавший не тот
// кадр, не должен выглядеть как «уже заменён» — клиент перечитал бы полосу и повторил ту же ошибку.
// Заменённый, спрятанный, стоящий на листе И разрезанный — already_replaced: слепой повтор перезаписи
// узнаёт себя по этому слову, даже если кадр после первой подачи успели спрятать, поставить на лист
// или разрезать. Спрятанный, стоящий на листе И разрезанный — hidden_picture: отказу, которому чтение
// не нужно, чтения не положено. Стоящий на листе И разрезанный — technical_sheet: лист отвечает одним
// чтением, и запрос, который он и так закрывает, за чтение ветки не платит.
//
// МУТАЦИИ: переставить любые две соседние проверки — одна из половин краснеет (спрятанность раньше
// already_replaced — вторая; спрятанность после листа — третья; лист после cut_sheet — четвёртая).
func TestDesignReplaceRefusalOrder(t *testing.T) {
	everything := DesignReplaceFacts{OnTechnicalSheet: true, StandingPieces: 3}
	hiddenAt := sql.NullTime{Time: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC), Valid: true}

	everythingWrong := replaceProbeOriginal()
	everythingWrong.TechCardId = replaceProbeCard + 1
	everythingWrong.MediaId = replaceProbeMedia + 1
	everythingWrong.ReplacedBy = sql.NullInt32{Int32: 12, Valid: true}
	everythingWrong.HiddenAt = hiddenAt
	require.ErrorIs(t,
		DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), everythingWrong, everything),
		ErrDesignReplaceMismatch)

	replacedHiddenOnTheSheetAndCut := replaceProbeOriginal()
	replacedHiddenOnTheSheetAndCut.ReplacedBy = sql.NullInt32{Int32: 12, Valid: true}
	replacedHiddenOnTheSheetAndCut.HiddenAt = hiddenAt
	err := DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), replacedHiddenOnTheSheetAndCut, everything)
	require.ErrorIs(t, err, ErrDesignAlreadyReplaced)
	require.NotErrorIs(t, err, ErrDesignHiddenPicture)
	require.NotErrorIs(t, err, ErrDesignTechnicalSheet)

	hiddenOnTheSheetAndCut := replaceProbeOriginal()
	hiddenOnTheSheetAndCut.HiddenAt = hiddenAt
	err = DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), hiddenOnTheSheetAndCut, everything)
	require.ErrorIs(t, err, ErrDesignHiddenPicture)
	require.NotErrorIs(t, err, ErrDesignTechnicalSheet)
	require.NotErrorIs(t, err, ErrDesignCutSheet)

	err = DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), replaceProbeOriginal(), everything)
	require.ErrorIs(t, err, ErrDesignTechnicalSheet)
	require.NotErrorIs(t, err, ErrDesignCutSheet)
}

// СПРЯТАННОСТЬ ЗВУЧИТ В ПЕРВОМ ПРОХОДЕ, А ЗАМЕНЁННОСТЬ ДЕРЖИТ ГОЛОВУ И ПОД НЕЙ (27.09).
//
// Стор зовёт DesignReplaceRefusal сначала с нулевыми фактами — до единого чтения — и already_replaced
// из этого прохода дописывает головой (DesignAlreadyReplaced). Проба проходит тот же путь: спрятанный
// кадр отказан уже с нулевыми фактами (листу и веткам спрашиваться незачем), а спрятанный И
// заменённый получает already_replaced с головой цепочки — повтор перезаписи без ключа находит
// правку, которую уже подал, хотя оригинал с тех пор спрятали.
//
// МУТАЦИИ: судить спрятанность только с прочитанными фактами (первый проход пропускает спрятанный
// кадр); поставить её раньше заменённости (второй случай теряет голову).
func TestDesignReplaceRefusalHiddenIsJudgedBeforeAnyRead(t *testing.T) {
	hiddenAt := sql.NullTime{Time: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC), Valid: true}

	hidden := replaceProbeOriginal()
	hidden.HiddenAt = hiddenAt
	require.ErrorIs(t,
		DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), hidden, DesignReplaceFacts{}),
		ErrDesignHiddenPicture, "нулевые факты: чтения ещё не было, а отказ уже звучит")

	chain, load, _ := replaceChain()
	hiddenReplaced := chain[7]
	hiddenReplaced.HiddenAt = hiddenAt
	err := DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(int32(hiddenReplaced.MediaId)), hiddenReplaced,
		DesignReplaceFacts{})
	require.ErrorIs(t, err, ErrDesignAlreadyReplaced)
	require.NotErrorIs(t, err, ErrDesignHiddenPicture)
	var replaced *DesignReplacedError
	require.ErrorAs(t, DesignAlreadyReplaced(hiddenReplaced, load), &replaced)
	require.Equal(t, 7, replaced.PictureId)
	require.Equal(t, 19, replaced.HeadPictureId, "спрятанность не отнимает у повтора голову")
}

// ─── ГОЛОВА ЦЕПОЧКИ ЗАМЕН (O-53 review) ───

// replaceChain — кадры цепочки 7 → 12 → 19 (19 — голова) и загрузчик по ним, считающий обращения.
func replaceChain() (map[int]DesignPicture, func(int) (DesignPicture, error), *int) {
	link := func(id, next int) DesignPicture {
		p := DesignPicture{Id: id, TechCardId: replaceProbeCard, MediaId: replaceProbeMedia + id}
		if next > 0 {
			p.ReplacedBy = sql.NullInt32{Int32: int32(next), Valid: true}
		}
		return p
	}
	chain := map[int]DesignPicture{7: link(7, 12), 12: link(12, 19), 19: link(19, 0)}
	calls := 0
	load := func(id int) (DesignPicture, error) {
		calls++
		p, ok := chain[id]
		if !ok {
			return DesignPicture{}, fmt.Errorf("%w: design picture %d", ErrDesignNotFound, id)
		}
		return p, nil
	}
	return chain, load, &calls
}

// ГОЛОВА — ПОСЛЕДНЕЕ ЗВЕНО, А НЕ ПЕРВАЯ ЗАМЕНА.
//
// МУТАЦИИ: вернуть replaced_by названного кадра вместо обхода (голова 12 вместо 19 — клиент открыл
// бы промежуточную правку, которую уже перезаписали); не читать незаменённый кадр как голову самого
// себя (лишнее чтение или ошибка на пустом месте).
func TestDesignReplacementHeadFollowsTheChainToItsEnd(t *testing.T) {
	chain, load, calls := replaceChain()

	head, err := DesignReplacementHead(chain[7], load)
	require.NoError(t, err)
	require.Equal(t, 19, head.Id, "голова — звено с replaced_by = NULL, а не первая замена")
	require.Equal(t, 2, *calls, "по одному чтению на звено")

	*calls = 0
	head, err = DesignReplacementHead(chain[19], load)
	require.NoError(t, err)
	require.Equal(t, 19, head.Id, "незаменённый кадр — сам себе голова")
	require.Zero(t, *calls, "и читать для этого нечего")
}

// ПОРЧА ЦЕПОЧКИ — НЕ ОТКАЗ И НЕ not_found.
//
// МУТАЦИИ: убрать проверку «следующий новее» (кольцо 7 → 12 → 7 крутится до потолка вместо того,
// чтобы быть названным на первом же шаге назад); завернуть ненайденное звено через %w (клиенту ушло
// бы not_found про кадр, которого он не называл); потерять %w у ошибки чтения прочего рода (дедлок
// 1213 перестал бы повторяться транзакцией); снять сверку карточки звена (голова чужой карточки
// ушла бы человеку как «picture #N стоит на месте вашего»).
func TestDesignReplacementHeadNamesACorruptChain(t *testing.T) {
	t.Run("ссылка назад", func(t *testing.T) {
		chain, load, calls := replaceChain()
		loop := chain[19]
		loop.ReplacedBy = sql.NullInt32{Int32: 7, Valid: true}
		chain[19] = loop
		_, err := DesignReplacementHead(chain[7], load)
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrDesignAlreadyReplaced)
		require.NotErrorIs(t, err, ErrDesignNotFound)
		require.Equal(t, 2, *calls, "шаг назад называется сразу, а не на потолке")
	})
	t.Run("звено не существует", func(t *testing.T) {
		chain, load, _ := replaceChain()
		delete(chain, 19)
		_, err := DesignReplacementHead(chain[7], load)
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrDesignNotFound, "not_found соврал бы о кадре, которого клиент не называл")
		require.Contains(t, err.Error(), "19")
	})
	t.Run("звено чужой карточки", func(t *testing.T) {
		chain, load, _ := replaceChain()
		foreign := chain[19]
		foreign.TechCardId = replaceProbeCard + 1
		chain[19] = foreign
		_, err := DesignReplacementHead(chain[7], load)
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrDesignAlreadyReplaced)
		require.NotErrorIs(t, err, ErrDesignNotFound)
		require.Contains(t, err.Error(), "leaves tech card 41 at picture 19")
		require.Error(t, DesignAlreadyReplaced(chain[7], load), "и отказ already_replaced с чужой головой не собирается")
		var replaced *DesignReplacedError
		require.False(t, errors.As(DesignAlreadyReplaced(chain[7], load), &replaced))
	})
	t.Run("чтение упало", func(t *testing.T) {
		chain, _, _ := replaceChain()
		transient := errors.New("Error 1213: Deadlock found when trying to get lock")
		_, err := DesignReplacementHead(chain[7], func(int) (DesignPicture, error) { return DesignPicture{}, transient })
		require.ErrorIs(t, err, transient, "ошибка чтения обязана остаться видимой для повтора транзакции")
	})
	t.Run("потолок", func(t *testing.T) {
		// Бесконечная, но честно растущая цепочка: каждый кадр заменён следующим по id.
		endless := func(id int) (DesignPicture, error) {
			return DesignPicture{Id: id, ReplacedBy: sql.NullInt32{Int32: int32(id + 1), Valid: true}}, nil
		}
		start, _ := endless(1)
		_, err := DesignReplacementHead(start, endless)
		require.Error(t, err)
		require.Contains(t, err.Error(), fmt.Sprintf("link %d", DesignReplacementChainMax))
	})
}

// ОТКАЗ already_replaced НЕСЁТ ГОЛОВУ — и остаётся already_replaced для всех, кто узнаёт его по
// сентинелу (таблица отказов хендлера, пробы стора).
//
// МУТАЦИИ: убрать Unwrap (errors.Is перестаёт узнавать отказ — хендлер отдал бы Internal); класть в
// HeadPictureId названный кадр или первую замену; вернуть отказ без головы, когда обход упал.
func TestDesignAlreadyReplacedCarriesTheHead(t *testing.T) {
	chain, load, _ := replaceChain()
	err := DesignAlreadyReplaced(chain[7], load)
	require.ErrorIs(t, err, ErrDesignAlreadyReplaced)
	var replaced *DesignReplacedError
	require.ErrorAs(t, fmt.Errorf("store: %w", err), &replaced, "голова переживает заворачивание")
	require.Equal(t, 7, replaced.PictureId)
	require.Equal(t, 19, replaced.HeadPictureId)
	require.Contains(t, err.Error(), "already_replaced")
	require.Contains(t, err.Error(), "picture 7")
	require.Contains(t, err.Error(), "picture 19")

	delete(chain, 19)
	err = DesignAlreadyReplaced(chain[7], load)
	require.Error(t, err)
	require.False(t, errors.As(err, &replaced), "отказ без головы нарушил бы обещание head_picture_id")
}

// ─── ЗЕРКАЛО technical_sheet НА СЕЙВЕ КАРТОЧКИ (27.09, D-57) ───

// sheetItem / boardItem — строки входящего сейва: лист и мудборд, как их собирает dto.
func sheetItem(media int) TechCardMediaItem {
	return TechCardMediaItem{MediaId: media, Category: TechCardMediaCategoryTechnical, Kind: TechCardMediaFront}
}

func boardItem(media int) TechCardMediaItem {
	return TechCardMediaItem{MediaId: media, Category: TechCardMediaCategoryMoodboard, Kind: TechCardMediaMoodboard}
}

// ОТКАЗ НАЗЫВАЕТ СТРОКУ ЛИСТА И ГОЛОВУ ЦЕПОЧКИ — ТЕМ КАНАЛОМ, КОТОРЫМ СЕЙВ УЖЕ ОТКАЗЫВАЕТ ПОИМЁННО.
//
// Кадр 7 заменён: 7 → 12 → 19. Его файл стоит ВТОРЫМ на листе и ещё раз на мудборде, а перед листом в
// списке сейва идут два файла мудборда — ровно как dto склеивает списки.
//
// МУТАЦИИ: назвать первую замену вместо головы (#12); считать место по общему списку, а не по листу
// (technical_media[3]); отдать человеку номер с нуля («item 1»); отказать строкой без поля (клиент не
// пришпилил бы отказ к плите и не открыл бы ARTIFACTS).
func TestDesignSheetReplacedRefusalNamesTheItemAndTheHead(t *testing.T) {
	chain, load, _ := replaceChain()
	replacedMedia := chain[7].MediaId
	media := []TechCardMediaItem{boardItem(500), boardItem(replacedMedia), sheetItem(600), sheetItem(replacedMedia)}

	err := DesignSheetReplacedRefusal(replaceProbeCard, media, nil, []DesignPicture{chain[7]}, load)
	var ve *ValidationError
	require.ErrorAs(t, err, &ve, "поимённый отказ сейва — ValidationError, а не второй канал")
	require.Equal(t, "technical_media[1].media_id", ve.Field)
	require.Equal(t, DesignSheetReplacedReason, ve.Reason)
	require.Equal(t, "replaced_picture", ve.Reason, "код причины на проводе не меняется молча")
	require.Empty(t, ve.Conflicting)
	require.Equal(t, "technical sheet item 2: this drawing was replaced by picture #19 — "+
		"put the replacement on the sheet, or take this one off", ve.HowToFix)
	require.Equal(t, "technical_media[1].media_id: replaced_picture; technical sheet item 2: this drawing was "+
		"replaced by picture #19 — put the replacement on the sheet, or take this one off", ve.Error())
}

// ЛИСТ ДЕРЖИТ ТОЛЬКО ЗАМЕНЁННЫЙ КАДР ЭТОЙ КАРТОЧКИ, И ТОЛЬКО НА ЛИСТЕ.
//
// Положительный контроль первым: те же входы с файлом заменённого кадра на листе отказывают — без него
// каждая половина ниже зеленела бы и на стороже, который не отказывает никогда.
//
// МУТАЦИИ: проверять и мудборд (половина «мудборд»); не смотреть на replaced_by (голова 19 — кадр
// этой карточки с тем же правом на лист, что у любого живого кадра, — отказывала бы); не смотреть на
// карточку (заменённый кадр чужой карточки закрывал бы свой файл на этом листе).
func TestDesignSheetReplacedRefusalLetsTheRestThrough(t *testing.T) {
	chain, load, _ := replaceChain()
	replaced := chain[7]
	foreign := DesignPicture{Id: 40, TechCardId: replaceProbeCard + 1, MediaId: 4040,
		ReplacedBy: sql.NullInt32{Int32: 41, Valid: true}}
	read := []DesignPicture{replaced, chain[19], foreign}

	require.Error(t, DesignSheetReplacedRefusal(replaceProbeCard, []TechCardMediaItem{sheetItem(replaced.MediaId)}, nil, read, load),
		"контроль: заменённый кадр этой карточки на листе — отказ")

	for _, tc := range []struct {
		name  string
		media []TechCardMediaItem
	}{
		{"мудборд", []TechCardMediaItem{boardItem(replaced.MediaId), sheetItem(600)}},
		{"голова цепочки на листе", []TechCardMediaItem{sheetItem(chain[19].MediaId)}},
		{"заменённый кадр чужой карточки", []TechCardMediaItem{sheetItem(foreign.MediaId)}},
		{"пустой сейв", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, DesignSheetReplacedRefusal(replaceProbeCard, tc.media, nil, read, load))
		})
	}
	t.Run("ничего не заменено — и цепочку не читать", func(t *testing.T) {
		_, load, calls := replaceChain()
		require.NoError(t, DesignSheetReplacedRefusal(replaceProbeCard, []TechCardMediaItem{sheetItem(replaced.MediaId)}, nil, nil, load))
		require.Zero(t, *calls)
	})
}

// ОДИН ФАЙЛ У ДВУХ ЗАМЕНЁННЫХ КАДРОВ — НАЗЫВАЕТСЯ СТАРШИЙ, В КАКОМ БЫ ПОРЯДКЕ ИХ НИ ПРОЧЛИ.
//
// МУТАЦИЯ: брать первый прочитанный — голова начинала бы зависеть от плана запроса.
func TestDesignSheetReplacedRefusalNamesTheOlderPictureOfOneFile(t *testing.T) {
	chain, load, _ := replaceChain()
	younger := DesignPicture{Id: 30, TechCardId: replaceProbeCard, MediaId: chain[7].MediaId,
		ReplacedBy: sql.NullInt32{Int32: 31, Valid: true}}
	chain[31] = DesignPicture{Id: 31, TechCardId: replaceProbeCard, MediaId: 3131}
	for _, read := range [][]DesignPicture{{younger, chain[7]}, {chain[7], younger}} {
		err := DesignSheetReplacedRefusal(replaceProbeCard, []TechCardMediaItem{sheetItem(chain[7].MediaId)}, nil, read, load)
		var ve *ValidationError
		require.ErrorAs(t, err, &ve)
		require.Contains(t, ve.HowToFix, "replaced by picture #19")
	}
}

// ПОРЧА ЦЕПОЧКИ — НЕ ПОИМЁННЫЙ ОТКАЗ, А ОШИБКА ЧТЕНИЯ ОСТАЁТСЯ ВИДИМОЙ ДЛЯ ПОВТОРА.
//
// МУТАЦИИ: отказать поимённо с головой, которую не удалось прочесть (человеку ушло бы «замените на
// #0»); потерять %w у ошибки чтения (дедлок 1213 перестал бы повторяться транзакцией сейва).
func TestDesignSheetReplacedRefusalKeepsCorruptionAndReadErrors(t *testing.T) {
	chain, load, _ := replaceChain()
	delete(chain, 19)
	err := DesignSheetReplacedRefusal(replaceProbeCard, []TechCardMediaItem{sheetItem(chain[7].MediaId)}, nil, []DesignPicture{chain[7]}, load)
	require.Error(t, err)
	var ve *ValidationError
	require.False(t, errors.As(err, &ve), "порча цепочки — не то, что человек чинит на листе")
	require.NotErrorIs(t, err, ErrDesignNotFound)

	chain, _, _ = replaceChain()
	transient := errors.New("Error 1213: Deadlock found when trying to get lock")
	err = DesignSheetReplacedRefusal(replaceProbeCard, []TechCardMediaItem{sheetItem(chain[7].MediaId)}, nil, []DesignPicture{chain[7]},
		func(int) (DesignPicture, error) { return DesignPicture{}, transient })
	require.ErrorIs(t, err, transient)
}

// СЕЙВ СУДИТ ПЕРЕХОД: УНАСЛЕДОВАННЫЙ ЛИСТ СОХРАНЯЕТСЯ, НОВОЕ ВХОЖДЕНИЕ ОТКАЗЫВАЕТ (D-57, раунд 3).
//
// Файл M заменённого кадра 7 уже стоит на листе один раз — лист собран до сторожа. Проба ведёт
// его через жизнь карточки: сохранить как есть, переставить, добавить вторую копию, снять, вернуть.
//
// МУТАЦИИ: снять сверку с сохранённым (снова судится весь входящий лист — первые две половины
// отказывают); сравнивать множества, а не счёт (одна унаследованная строка разрешает вторую копию —
// третья половина проходит); назвать первое вхождение файла вместо первого СВЕРХ сохранённого числа
// (третья называет item 1).
//
// МЕСТО, А НЕ ЛИЧНОСТЬ. Называется первое вхождение сверх сохранённого числа в порядке технического
// списка: у строк листа нет ключа, и копию, вставленную ПЕРЕД прежней, отказ называет по месту второй
// (последняя половина держит этот контракт, чтобы он не поменялся молча).
func TestDesignSheetReplacedRefusalJudgesTheTransition(t *testing.T) {
	chain, load, _ := replaceChain()
	m := chain[7].MediaId
	read := []DesignPicture{chain[7]}
	legacy := map[int]int{m: 1}

	for _, tc := range []struct {
		name  string
		media []TechCardMediaItem
	}{
		{"унаследованная строка как есть", []TechCardMediaItem{sheetItem(m)}},
		{"переставлена за другой файл", []TechCardMediaItem{sheetItem(600), sheetItem(m)}},
		{"снята", []TechCardMediaItem{sheetItem(600)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, DesignSheetReplacedRefusal(replaceProbeCard, tc.media, legacy, read, load))
		})
	}
	t.Run("вторая копия — отказ первому вхождению сверх сохранённого числа", func(t *testing.T) {
		for _, media := range [][]TechCardMediaItem{
			{sheetItem(m), sheetItem(600), sheetItem(m)},
			{sheetItem(600), sheetItem(m), sheetItem(m)},
		} {
			err := DesignSheetReplacedRefusal(replaceProbeCard, media, legacy, read, load)
			var ve *ValidationError
			require.ErrorAs(t, err, &ve)
			require.Equal(t, "technical_media[2].media_id", ve.Field, "называется вхождение сверх сохранённого числа")
			require.Contains(t, ve.HowToFix, "technical sheet item 3: this drawing was replaced by picture #19")
		}
	})
	t.Run("новая копия ВПЕРЕДИ прежней — названа вторая по месту", func(t *testing.T) {
		added := sheetItem(m)
		added.Kind = TechCardMediaBack // человек положил новую копию первой, другим видом
		err := DesignSheetReplacedRefusal(replaceProbeCard, []TechCardMediaItem{added, sheetItem(m)}, legacy, read, load)
		var ve *ValidationError
		require.ErrorAs(t, err, &ve)
		require.Equal(t, "technical_media[1].media_id", ve.Field,
			"первое вхождение сверх сохранённого числа в порядке списка — место, а не личность копии")
		require.Contains(t, ve.HowToFix, "technical sheet item 2: this drawing was replaced by picture #19")
	})
	t.Run("снятый файл обратно не встаёт", func(t *testing.T) {
		err := DesignSheetReplacedRefusal(replaceProbeCard, []TechCardMediaItem{sheetItem(600), sheetItem(m)},
			map[int]int{600: 1}, read, load)
		var ve *ValidationError
		require.ErrorAs(t, err, &ve)
		require.Equal(t, "technical_media[1].media_id", ve.Field)
	})
	t.Run("сохранённое число другого файла ничего не разрешает", func(t *testing.T) {
		err := DesignSheetReplacedRefusal(replaceProbeCard, []TechCardMediaItem{sheetItem(m)}, map[int]int{600: 5}, read, load)
		require.Error(t, err)
	})
}

// ЧТЕНИЕ СПРАШИВАЕТ ТОЛЬКО ФАЙЛЫ ЛИСТА, КАЖДЫЙ ОДИН РАЗ, В ПОРЯДКЕ ЛИСТА.
//
// МУТАЦИЯ: спрашивать и мудборд — стор читал и запирал бы кадры, которые правило не судит.
func TestDesignSheetMediaIdsAskOnlyTheSheet(t *testing.T) {
	require.Equal(t, []int{600, 900, 700},
		DesignSheetMediaIds([]TechCardMediaItem{boardItem(500), sheetItem(600), boardItem(900), sheetItem(900), sheetItem(600), sheetItem(700)}))
	require.Empty(t, DesignSheetMediaIds([]TechCardMediaItem{boardItem(500)}))
}

// ─── СТОИТ ЛИ КУСОК: ВСЯ ВЕТКА (O-53 review, раунд 3) ───

// standingSheet — лист, чьи куски судятся: 7, на виду, не заменён.
const standingSheet = 7

func shownNode(id int) DesignBranchNode {
	return DesignBranchNode{Id: id, TechCardId: replaceProbeCard}
}

func hiddenNode(id int) DesignBranchNode {
	return DesignBranchNode{Id: id, TechCardId: replaceProbeCard, HiddenAt: sql.NullTime{Valid: true}}
}

// onCard — n лежит на карточке card: порча, которую ни один писатель не делает (раунд 4).
func onCard(n DesignBranchNode, card int) DesignBranchNode {
	n.TechCardId = card
	return n
}

// cutFrom — n отрезан от parent: кроп.
func cutFrom(n DesignBranchNode, parent int) DesignBranchNode {
	n.DerivedFrom = sql.NullInt32{Int32: int32(parent), Valid: true}
	n.Derivation = DesignDerivationCrop
	return n
}

// editOf — n — правка parent: флэттен. «На месте» его делает replacedBy у parent, без него — «рядом».
func editOf(n DesignBranchNode, parent int) DesignBranchNode {
	n.DerivedFrom = sql.NullInt32{Int32: int32(parent), Valid: true}
	n.Derivation = DesignDerivationFlatten
	return n
}

// legacyOf — n — ребёнок parent с пустым глаголом: строка, которую бэкфилл 0359 не классифицировал.
func legacyOf(n DesignBranchNode, parent int) DesignBranchNode {
	n.DerivedFrom = sql.NullInt32{Int32: int32(parent), Valid: true}
	return n
}

// replacedBy — место n заняла правка next.
func replacedBy(n DesignBranchNode, next int) DesignBranchNode {
	n.ReplacedBy = sql.NullInt32{Int32: int32(next), Valid: true}
	return n
}

// standingCard — все кадры карточки: лист 7, ветка случая и ШУМ, который не держит лист ни в одном
// случае. В шуме на виду стоят правка листа «рядом» (8), легаси-ребёнок листа (9) и кусок ДРУГОГО
// листа (4 от 3), а спрятанный кадр 2 ни к чему не привязан. Поэтому каждый случай «отпускает»
// заодно проверяет, что обход не берёт куском правку, легаси или чужой кусок.
func standingCard(branch ...DesignBranchNode) []DesignBranchNode {
	nodes := []DesignBranchNode{
		hiddenNode(2),
		shownNode(3),
		cutFrom(shownNode(4), 3),
		shownNode(standingSheet),
		editOf(shownNode(8), standingSheet),
		legacyOf(shownNode(9), standingSheet),
	}
	return append(nodes, branch...)
}

// standingCase — один кусок 10 листа 7 и его ветка; holds — держит ли он лист.
type standingCase struct {
	name   string
	branch []DesignBranchNode
	holds  bool
}

// standingCases — таблица случаев «стоит ли кусок». Одна на две пробы: обход по всей карточке
// (TestDesignStandingPiecesJudgeTheWholeBranch) и обход по тому, что прочитало чтение ветки
// (TestDesignLoadBranchReadsWhatTheWalkNeeds), — ответы обязаны совпасть случай в случай.
func standingCases() []standingCase {
	return []standingCase{
		{"кусок на виду", []DesignBranchNode{
			cutFrom(shownNode(10), standingSheet),
		}, true},
		{"кусок спрятан, под ним ничего", []DesignBranchNode{
			cutFrom(hiddenNode(10), standingSheet),
		}, false},
		{"кусок спрятан, его правка на виду — устаревшая вкладка раунда 2", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			editOf(shownNode(11), 10),
		}, true},
		{"кусок на виду, его правка спрятана — сам кусок стоит", []DesignBranchNode{
			replacedBy(cutFrom(shownNode(10), standingSheet), 11),
			editOf(hiddenNode(11), 10),
		}, true},
		{"сценарий Codex: C на виду, его правка E спрятана, F отрезан от E и на виду", []DesignBranchNode{
			replacedBy(cutFrom(shownNode(10), standingSheet), 11),
			editOf(hiddenNode(11), 10),
			cutFrom(shownNode(12), 11),
		}, true},
		{"спрятаны C и E, F отрезан от спрятанной головы и на виду", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			editOf(hiddenNode(11), 10),
			cutFrom(shownNode(12), 11),
		}, true},
		{"спрятано всё: C, E и F", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			editOf(hiddenNode(11), 10),
			cutFrom(hiddenNode(12), 11),
		}, false},
		{"кусок куска на виду", []DesignBranchNode{
			cutFrom(hiddenNode(10), standingSheet),
			cutFrom(shownNode(11), 10),
		}, true},
		{"кусок куска спрятан", []DesignBranchNode{
			cutFrom(hiddenNode(10), standingSheet),
			cutFrom(hiddenNode(11), 10),
		}, false},
		{"правка куска рядом не держит", []DesignBranchNode{
			cutFrom(hiddenNode(10), standingSheet),
			editOf(shownNode(11), 10),
		}, false},
		{"легаси-ребёнок куска не держит", []DesignBranchNode{
			cutFrom(hiddenNode(10), standingSheet),
			legacyOf(shownNode(11), 10),
		}, false},
		{"две правки: голова спрятана, промежуточная на виду", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			replacedBy(editOf(shownNode(11), 10), 12),
			editOf(hiddenNode(12), 11),
		}, true},
		{"две правки: первая спрятана, голова на виду", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			replacedBy(editOf(hiddenNode(11), 10), 12),
			editOf(shownNode(12), 11),
		}, true},
		{"длинная ветка: замена, разрез, замена, разрез — на виду только последний", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			editOf(hiddenNode(11), 10),
			replacedBy(cutFrom(hiddenNode(12), 11), 13),
			editOf(hiddenNode(13), 12),
			cutFrom(shownNode(14), 13),
		}, true},
		{"длинная ветка спрятана до конца", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			editOf(hiddenNode(11), 10),
			replacedBy(cutFrom(hiddenNode(12), 11), 13),
			editOf(hiddenNode(13), 12),
			cutFrom(hiddenNode(14), 13),
		}, false},
	}
}

// ЛИСТ ДЕРЖИТ КУСОК, ПОКА НА ЭКРАНЕ ХОТЬ ЧТО-ТО ИЗ ЕГО ВЕТКИ.
//
// Каждый случай — ОДИН кусок 10 и его ветка, и ответ сверяется по куску, а не по счёту: общий счёт
// скрыл бы мутанта, у которого два неверных ответа складываются в верную сумму.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ:
//   - судить кусок по его строке — «спрятанный кусок, правка на виду», «спрятаны C и E, F на виду»,
//     «кусок куска», длинная ветка;
//   - судить по голове цепочки замен (раунд 2) — «кусок на виду, правка спрятана», сценарий Codex,
//     «спрятаны C и E», «кусок куска», «голова спрятана, промежуточная правка на виду»;
//   - выбросить ребро разреза (идти только по replaced_by) — «спрятаны C и E», «кусок куска», длинная
//     ветка;
//   - брать куском любого ребёнка, а не кроп, — «правка куска рядом», «легаси-ребёнок куска» и шум
//     standingCard в каждом отпускающем случае.
func TestDesignStandingPiecesJudgeTheWholeBranch(t *testing.T) {
	for _, tc := range standingCases() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DesignStandingPieces(standingSheet, standingCard(tc.branch...))
			require.NoError(t, err)
			if tc.holds {
				require.Equal(t, []int{10}, got, "кусок 10 стоит, и держит лист только он")
			} else {
				require.Empty(t, got, "от куска 10 на экране не осталось ничего")
			}
		})
	}
}

// НЕСКОЛЬКО КУСКОВ СУДЯТСЯ КАЖДЫЙ СВОЕЙ ВЕТКОЙ, И ПОРЯДОК ВХОДА НЕ ЗНАЧИТ НИЧЕГО.
//
// Ответ — ровно стоящие куски по возрастанию id, а не «первый стоящий» и не счёт.
func TestDesignStandingPiecesNameEveryStandingPiece(t *testing.T) {
	branch := []DesignBranchNode{
		cutFrom(shownNode(30), standingSheet),
		editOf(shownNode(21), 20),
		cutFrom(hiddenNode(10), standingSheet),
		replacedBy(cutFrom(hiddenNode(20), standingSheet), 21),
	}
	got, err := DesignStandingPieces(standingSheet, standingCard(branch...))
	require.NoError(t, err)
	require.Equal(t, []int{20, 30}, got)

	nodes := standingCard(branch...)
	for i, j := 0, len(nodes)-1; i < j; i, j = i+1, j-1 {
		nodes[i], nodes[j] = nodes[j], nodes[i]
	}
	got, err = DesignStandingPieces(standingSheet, nodes)
	require.NoError(t, err)
	require.Equal(t, []int{20, 30}, got, "порядок строк SELECT ответа не меняет")
}

// ПОРЧА — ОШИБКА, А НЕ ОТВЕТ «ОТПУСКАЕТ».
//
// Ни одна ошибка обхода не несёт сентинела полосы: клиенту Internal, а не cut_sheet и не not_found про
// кадр, которого он не называл.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ:
//   - проглотить потерянный кадр (считать его спрятанным и пустым) — «замена ведёт мимо карточки»
//     отпускает лист;
//   - снять сверку «ребро ведёт к новому кадру» — «ссылка на старый спрятанный кадр» уходит в чужой
//     спрятанный кадр и отпускает лист;
//   - снять visited — «кадр достигнут дважды» проходится дважды и отпускает лист (все рёбра там идут
//     вперёд, и сверка порядка его не видит);
//   - снять сверку карточки (раунд 4) — «видимый кусок другой карточки» держит лист молча, а
//     «замена на кадр другой карточки» отпускает его.
func TestDesignStandingPiecesRefuseABrokenBranch(t *testing.T) {
	for _, tc := range brokenBranchCases() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DesignStandingPieces(standingSheet, standingCard(tc.branch...))
			require.Error(t, err, "порча не выдаётся за ответ (%v)", got)
			require.Contains(t, err.Error(), tc.names)
			for _, sentinel := range []error{ErrDesignCutSheet, ErrDesignNotFound, ErrDesignInvalidArgument, ErrDesignAlreadyReplaced} {
				require.NotErrorIs(t, err, sentinel, "порча — Internal, а не отказ полосы")
			}
		})
	}

	t.Run("лист не прочитан", func(t *testing.T) {
		_, err := DesignStandingPieces(standingSheet, []DesignBranchNode{cutFrom(shownNode(10), standingSheet)})
		require.Error(t, err)
	})

	// Обратная сторона: ВИДИМЫЙ кусок держит лист, и порча за ним ответа не меняет — отказ закрывает
	// дверь и без неё. Иначе потерянная ссылка превращала бы честный cut_sheet в Internal.
	t.Run("порча за видимым кадром", func(t *testing.T) {
		got, err := DesignStandingPieces(standingSheet, standingCard(
			replacedBy(cutFrom(shownNode(10), standingSheet), 99),
		))
		require.NoError(t, err)
		require.Equal(t, []int{10}, got)
	})
}

// brokenBranchCase — ветка куска 10 с порчей; names — что обязана назвать ошибка; onRead — порча,
// на которой отказывает уже чтение ветки (кадр другой карточки, раунд 5), а не обход по прочитанному.
type brokenBranchCase struct {
	name   string
	branch []DesignBranchNode
	names  string
	onRead bool
}

// brokenBranchCases — порча ветки. Одна таблица на обход по всей карточке и на обход по чтению ветки.
func brokenBranchCases() []brokenBranchCase {
	return []brokenBranchCase{
		{"замена ведёт мимо карточки", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 99),
		}, "picture 99", false},
		{"замена ведёт назад, на сам лист", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), standingSheet),
		}, "picture 7", false},
		{"ссылка на старый спрятанный кадр", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 2),
		}, "picture 2", false},
		{"цикл замен", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			replacedBy(editOf(hiddenNode(11), 10), 10),
		}, "picture 11", false},
		{"кадр достигнут дважды", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 12),
			cutFrom(hiddenNode(11), 10),
			cutFrom(hiddenNode(12), 11),
		}, "picture 12 is reached twice", false},
		// Раунд 4: кроп с derived_from = лист на ЧУЖОЙ карточке. Скан по карточке его не видел, и
		// видимый кусок молча не держал лист; теперь он прочитан и назван.
		{"видимый кусок другой карточки", []DesignBranchNode{
			onCard(cutFrom(shownNode(10), standingSheet), replaceProbeCard+1),
		}, "belongs to tech card 42", true},
		{"замена на кадр другой карточки", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			onCard(editOf(hiddenNode(11), 10), replaceProbeCard+1),
		}, "picture 11, reached from picture 10, belongs to tech card 42", true},
	}
}

// standingLine — кусок first листа 7 и n-1 спрятанных звеньев под ним, замена и разрез по очереди;
// всё спрятано, так что обход обязан пройти ветку до конца.
func standingLine(first, n int) []DesignBranchNode {
	line := []DesignBranchNode{cutFrom(hiddenNode(first), standingSheet)}
	for i := 1; i < n; i++ {
		id, prev := first+i, first+i-1
		if i%2 == 1 {
			line[i-1] = replacedBy(line[i-1], id)
			line = append(line, editOf(hiddenNode(id), prev))
		} else {
			line = append(line, cutFrom(hiddenNode(id), prev))
		}
	}
	return line
}

// ПОТОЛОК — ОБЩИЙ НА ЗАПРОС, И ЛИСТ — ПЕРВЫЙ ИЗ НЕГО.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: потолок на ветку вместо запроса (два куска по половине потолка проходят);
// сдвиг границы на единицу (ровно потолок отказывается либо потолок плюс один проходит).
func TestDesignStandingPiecesStopAtTheTotalCeiling(t *testing.T) {
	sheet := shownNode(standingSheet)
	t.Run("ровно потолок", func(t *testing.T) {
		nodes := append([]DesignBranchNode{sheet}, standingLine(10, DesignStandingNodesMax-1)...)
		got, err := DesignStandingPieces(standingSheet, nodes)
		require.NoError(t, err)
		require.Empty(t, got)
	})
	t.Run("на один кадр больше", func(t *testing.T) {
		nodes := append([]DesignBranchNode{sheet}, standingLine(10, DesignStandingNodesMax)...)
		_, err := DesignStandingPieces(standingSheet, nodes)
		require.Error(t, err)
		require.Contains(t, err.Error(), fmt.Sprintf("more than %d pictures", DesignStandingNodesMax))
		require.NotErrorIs(t, err, ErrDesignCutSheet)
	})
	t.Run("два куска, каждый под потолком, вместе над ним", func(t *testing.T) {
		half := DesignStandingNodesMax / 2
		nodes := append([]DesignBranchNode{sheet}, standingLine(10, half)...)
		nodes = append(nodes, standingLine(10+half, half)...)
		_, err := DesignStandingPieces(standingSheet, nodes)
		require.Error(t, err, "лист и две ветки по %d — это %d кадров", half, 1+2*half)
		require.Contains(t, err.Error(), fmt.Sprintf("more than %d pictures", DesignStandingNodesMax))
	})
}

// КОЛОНКИ ЗАПРОСА — РОВНО ПОЛЯ УЗЛА, В ТОМ ЖЕ ПОРЯДКЕ.
//
// МУТАЦИИ: выбросить колонку из DesignBranchColumns (sqlx молча оставил бы поле нулём — без
// derivation, derived_from или replaced_by обход не видит рёбер и отпускает лист); завести поле, не
// выбранное запросом.
func TestDesignBranchColumnsAreTheNodeFields(t *testing.T) {
	typ := reflect.TypeOf(DesignBranchNode{})
	tags := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("db")
		require.NotEmpty(t, tag, "поле %s без колонки", typ.Field(i).Name)
		tags = append(tags, tag)
	}
	require.Equal(t, strings.Join(tags, ", "), DesignBranchColumns)
}

// ЛИСТ СТАНОВИТСЯ УЗЛОМ ОБХОДА ЦЕЛИКОМ: каждое поле узла взято из одноимённого поля кадра.
//
// МУТАЦИЯ: забыть поле в DesignBranchNodeOf — прежде всего tech_card_id (лист с карточкой 0 сделал бы
// чужим каждый кадр своей ветки) или replaced_by.
func TestDesignBranchNodeOfCopiesEveryField(t *testing.T) {
	p := DesignPicture{
		Id: 7, TechCardId: replaceProbeCard, Derivation: DesignDerivationCrop,
		HiddenAt:    sql.NullTime{Time: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC), Valid: true},
		ReplacedBy:  sql.NullInt32{Int32: 12, Valid: true},
		DerivedFrom: sql.NullInt32{Int32: 3, Valid: true},
	}
	n := DesignBranchNodeOf(p)
	nv, pv := reflect.ValueOf(n), reflect.ValueOf(p)
	for i := 0; i < nv.NumField(); i++ {
		name := nv.Type().Field(i).Name
		src := pv.FieldByName(name)
		require.True(t, src.IsValid(), "у кадра нет поля %s", name)
		require.False(t, src.IsZero(), "проба обязана заполнить %s кадра", name)
		require.Equal(t, src.Interface(), nv.Field(i).Interface(), "поле %s", name)
	}
}

// СПРЯТАННЫЙ КАДР НЕ РЕЖЕТСЯ — СВОИМ СЛОВОМ, А НЕ hidden_plate.
//
// МУТАЦИИ: судить не по hidden_at (спрятанный проходит); отдать сентинел постановки в слот (клиент
// показал бы «плиту нельзя поставить» на жест разреза).
func TestDesignSplitHiddenRefusal(t *testing.T) {
	shown := replaceProbeOriginal()
	require.NoError(t, DesignSplitHiddenRefusal(shown))

	hidden := replaceProbeOriginal()
	hidden.HiddenAt = sql.NullTime{Valid: true}
	err := DesignSplitHiddenRefusal(hidden)
	require.ErrorIs(t, err, ErrDesignHiddenPicture)
	require.NotErrorIs(t, err, ErrDesignHiddenPlate)
	require.Contains(t, err.Error(), "hidden_picture")
	require.Contains(t, err.Error(), "picture 7")
}

// ─── ЧТЕНИЕ ВЕТКИ УРОВНЯМИ (O-53 review, раунд 4) ───

// branchCall — один вызов чтения: какое чтение, какие id, какой limit.
type branchCall struct {
	read  string // "id" | "crops"
	ids   []int
	limit int
}

// branchTable — design_picture в памяти, отвечающая на DesignBranchReads так же, как SQL стора: БЕЗ
// предиката карточки, в порядке запроса (цели замен — по id, кропы — по derived_from, id), не больше
// limit строк (LIMIT). calls — каждый вызов по порядку.
type branchTable struct {
	rows  map[int]DesignBranchNode
	crops map[int][]DesignBranchNode // derived_from → кропы
	calls []branchCall
	fail  error
}

func newBranchTable(nodes ...DesignBranchNode) *branchTable {
	b := &branchTable{rows: map[int]DesignBranchNode{}, crops: map[int][]DesignBranchNode{}}
	for _, n := range nodes {
		b.rows[n.Id] = n
		if n.DerivedFrom.Valid && n.Derivation == DesignDerivationCrop {
			parent := int(n.DerivedFrom.Int32)
			b.crops[parent] = append(b.crops[parent], n)
		}
	}
	return b
}

func (b *branchTable) reads() DesignBranchReads {
	answer := func(read string, ids []int, limit int, rows []DesignBranchNode) ([]DesignBranchNode, error) {
		b.calls = append(b.calls, branchCall{read: read, ids: append([]int(nil), ids...), limit: limit})
		if b.fail != nil {
			return nil, b.fail
		}
		if len(rows) > limit {
			rows = rows[:limit]
		}
		return rows, nil
	}
	return DesignBranchReads{
		ByID: func(ids []int, limit int) ([]DesignBranchNode, error) {
			var rows []DesignBranchNode
			for _, id := range ids {
				if n, ok := b.rows[id]; ok {
					rows = append(rows, n)
				}
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].Id < rows[j].Id })
			return answer("id", ids, limit, rows)
		},
		CropsOf: func(parents []int, limit int) ([]DesignBranchNode, error) {
			var rows []DesignBranchNode
			for _, p := range parents {
				rows = append(rows, b.crops[p]...)
			}
			sort.Slice(rows, func(i, j int) bool {
				if rows[i].DerivedFrom.Int32 != rows[j].DerivedFrom.Int32 {
					return rows[i].DerivedFrom.Int32 < rows[j].DerivedFrom.Int32
				}
				return rows[i].Id < rows[j].Id
			})
			return answer("crops", parents, limit, rows)
		},
	}
}

// loadedIDs — id прочитанного набора, по возрастанию.
func loadedIDs(nodes []DesignBranchNode) []int {
	ids := make([]int, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.Id)
	}
	sort.Ints(ids)
	return ids
}

// ЧТЕНИЕ ВЕТКИ ДАЁТ ОБХОДУ РОВНО ТО, ЧТО ЕМУ НУЖНО — И НИЧЕГО ИЗ ШУМА КАРТОЧКИ.
//
// Каждый случай обеих таблиц проходит дважды: обход по всей карточке и обход по набору, который
// собрало чтение из той же карточки. Ответы обязаны совпасть — вердикт в случаях «стоит ли кусок» и
// порча, названная ТЕМИ ЖЕ СЛОВАМИ, в случаях порчи (кадр другой карточки называет уже чтение, раунд 5;
// прочую — обход по прочитанному), — а шум карточки (чужой лист 3 и его кусок 4, ничей спрятанный 2,
// правка листа «рядом» 8, легаси-ребёнок листа 9) не читается ни разу, кроме случая, где на кадр 2
// ведёт порченая замена.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: остановиться после первого уровня или не читать кропы глубже листа
// («кусок куска на виду» отпускает лист — ребро разреза пропадает МОЛЧА, это единственное ребро,
// потерю которого обход не видит); не читать замены (случаи со стоящей правкой отпускают лист или
// падают потерянным кадром); читать детей без глагола (в набор попадают 8 и 9); читать за видимым
// кадром (раунд 5 — у сценария Codex лишний вызов «кропы 12»); назвать чужой кадр не тем, откуда
// чтение к нему пришло.
func TestDesignLoadBranchReadsWhatTheWalkNeeds(t *testing.T) {
	sheet := shownNode(standingSheet)
	noise := []int{3, 4, 8, 9}
	for _, tc := range standingCases() {
		t.Run(tc.name, func(t *testing.T) {
			card := standingCard(tc.branch...)
			loaded, err := DesignLoadBranch(sheet, newBranchTable(card...).reads())
			require.NoError(t, err)
			require.Equal(t, standingSheet, loaded[0].Id, "лист — первый в наборе")
			ids := loadedIDs(loaded)
			for _, id := range append(noise, 2) {
				require.NotContains(t, ids, id, "шум карточки не читается")
			}
			whole, err := DesignStandingPieces(standingSheet, card)
			require.NoError(t, err)
			got, err := DesignStandingPieces(standingSheet, loaded)
			require.NoError(t, err)
			require.Equal(t, whole, got, "обход по прочитанному отвечает так же, как по всей карточке")
		})
	}
	for _, tc := range brokenBranchCases() {
		t.Run(tc.name, func(t *testing.T) {
			card := standingCard(tc.branch...)
			_, whole := DesignStandingPieces(standingSheet, card)
			require.Error(t, whole)
			loaded, err := DesignLoadBranch(sheet, newBranchTable(card...).reads())
			if tc.onRead {
				require.Error(t, err, "кадр другой карточки отказывается на чтении, а не дочитывается")
			} else {
				require.NoError(t, err, "прочую порчу чтение дочитывает и останавливается — судит её обход")
				for _, id := range noise {
					require.NotContains(t, loadedIDs(loaded), id)
				}
				_, err = DesignStandingPieces(standingSheet, loaded)
			}
			require.EqualError(t, err, whole.Error(), "порча называется теми же словами, кто бы её ни нашёл")
			require.Contains(t, err.Error(), tc.names)
		})
	}

	// Сценарий Codex, прочитанный уровнями: C (10) и его замена E (11) спрятаны, F (12) отрезан от E.
	codex := standingCard(
		replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
		editOf(hiddenNode(11), 10),
		cutFrom(shownNode(12), 11),
	)
	table := newBranchTable(codex...)
	loaded, err := DesignLoadBranch(sheet, table.reads())
	require.NoError(t, err)
	require.Equal(t, []int{7, 10, 11, 12}, loadedIDs(loaded))
	require.Equal(t, []branchCall{
		{read: "crops", ids: []int{7}, limit: DesignStandingNodesMax},
		{read: "id", ids: []int{11}, limit: DesignStandingNodesMax - 1},
		{read: "crops", ids: []int{10}, limit: DesignStandingNodesMax - 2},
		{read: "crops", ids: []int{11}, limit: DesignStandingNodesMax - 2},
	}, table.calls, "уровень за уровнем: кропы листа, затем замены и кропы спрятанных кадров уровня; "+
		"F (12) на виду, и за ним не читается ничего")
}

// ЧТЕНИЕ ОСТАНАВЛИВАЕТСЯ ТАМ ЖЕ, ГДЕ ОБХОД (O-53 review, раунд 5).
//
// Видимый кадр решает кусок, и ни его замена, ни его кропы не читаются; спрятанный раскрывается
// дальше, по обоим рёбрам; кадр другой карточки — отказ на том чтении, которое его принесло.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: читать за видимым кадром («8191 перезапись» упирается в потолок, и лишние
// вызовы у цепочки); не раскрывать спрятанную правку или её кропы (цепочка не доходит до видимого
// кадра, и набор теряет 12 и 14); снять отказ на чтении чужого кадра (чтение идёт в чужую карточку —
// лишний вызов «кропы 11» — и возвращает набор вместо ошибки).
func TestDesignLoadBranchStopsWhereTheWalkStops(t *testing.T) {
	sheet := shownNode(standingSheet)
	t.Run("видимый кусок и 8191 перезапись за ним", func(t *testing.T) {
		// Законный граф: перезапись ничего не прячет, и каждая правка занимает место предыдущей.
		branch := []DesignBranchNode{cutFrom(shownNode(10), standingSheet)}
		for i := 1; i < DesignStandingNodesMax; i++ {
			branch[i-1] = replacedBy(branch[i-1], 10+i)
			branch = append(branch, editOf(shownNode(10+i), 10+i-1))
		}
		card := standingCard(branch...)
		require.Greater(t, 1+len(branch), DesignStandingNodesMax, "лист и вся ветка куска — выше потолка")
		whole, err := DesignStandingPieces(standingSheet, card)
		require.NoError(t, err)
		require.Equal(t, []int{10}, whole)

		table := newBranchTable(card...)
		loaded, err := DesignLoadBranch(sheet, table.reads())
		require.NoError(t, err, "за видимым куском чтение не идёт и в потолок не упирается")
		require.Equal(t, []int{standingSheet, 10}, loadedIDs(loaded))
		require.Equal(t, []branchCall{
			{read: "crops", ids: []int{standingSheet}, limit: DesignStandingNodesMax},
		}, table.calls, "одно чтение — кропы листа; ни замена видимого куска, ни его кропы не запрашиваются")
		got, err := DesignStandingPieces(standingSheet, loaded)
		require.NoError(t, err)
		require.Equal(t, whole, got)
	})
	t.Run("спрятанная цепочка раскрывается до видимого кадра", func(t *testing.T) {
		card := standingCard(
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			replacedBy(editOf(hiddenNode(11), 10), 12),
			replacedBy(editOf(shownNode(12), 11), 13), // на виду: здесь обход решает кусок
			editOf(hiddenNode(13), 12),                // замена видимого кадра — не читается
			cutFrom(hiddenNode(14), 11),               // кроп спрятанной правки — читается
			cutFrom(shownNode(15), 12),                // кроп видимого кадра — не читается
		)
		whole, err := DesignStandingPieces(standingSheet, card)
		require.NoError(t, err)
		require.Equal(t, []int{10}, whole)

		table := newBranchTable(card...)
		loaded, err := DesignLoadBranch(sheet, table.reads())
		require.NoError(t, err)
		require.Equal(t, []int{7, 10, 11, 12, 14}, loadedIDs(loaded))
		require.Equal(t, []branchCall{
			{read: "crops", ids: []int{7}, limit: DesignStandingNodesMax},
			{read: "id", ids: []int{11}, limit: DesignStandingNodesMax - 1},
			{read: "crops", ids: []int{10}, limit: DesignStandingNodesMax - 2},
			{read: "id", ids: []int{12}, limit: DesignStandingNodesMax - 2},
			{read: "crops", ids: []int{11}, limit: DesignStandingNodesMax - 3},
			{read: "crops", ids: []int{14}, limit: DesignStandingNodesMax - 4},
		}, table.calls)
		got, err := DesignStandingPieces(standingSheet, loaded)
		require.NoError(t, err)
		require.Equal(t, whole, got)
	})
	t.Run("кадр другой карточки — отказ на чтении, за ним не читается ничего", func(t *testing.T) {
		card := standingCard(
			cutFrom(hiddenNode(10), standingSheet),
			onCard(cutFrom(hiddenNode(11), 10), replaceProbeCard+1),
			onCard(cutFrom(shownNode(12), 11), replaceProbeCard+1),
		)
		_, whole := DesignStandingPieces(standingSheet, card)
		require.Error(t, whole)

		table := newBranchTable(card...)
		_, err := DesignLoadBranch(sheet, table.reads())
		require.EqualError(t, err, whole.Error())
		require.Contains(t, err.Error(), "picture 11, reached from picture 10, belongs to tech card 42, not to tech card 41")
		for _, sentinel := range []error{ErrDesignCutSheet, ErrDesignNotFound, ErrDesignInvalidArgument, ErrDesignAlreadyReplaced} {
			require.NotErrorIs(t, err, sentinel, "порча — Internal, а не отказ полосы")
		}
		require.Equal(t, []branchCall{
			{read: "crops", ids: []int{7}, limit: DesignStandingNodesMax},
			{read: "crops", ids: []int{10}, limit: DesignStandingNodesMax - 1},
		}, table.calls, "кропы чужого кадра не запрашиваются")
	})
}

// ШИРОКИЙ УРОВЕНЬ ЧИТАЕТСЯ КУСКАМИ ПО DesignBranchChunk id.
//
// Лист с 2·DesignBranchChunk+1 спрятанными кусками, у каждого — спрятанная замена: на первом уровне
// столько же целей замены и родителей кропов, на втором — родителей кропов. Каждый вызов называет не
// больше DesignBranchChunk id, id идут по возрастанию, и вместе вызовы одного чтения покрывают
// уровень целиком и без повторов. Сам кусок СТРОГО уже eq_range_index_dive_limit MySQL по умолчанию
// (200): погружения гарантированы до 199 равенств включительно — см. DesignBranchChunk.
//
// МУТАЦИИ: снять разбиение (один вызов на весь уровень); поднять кусок до 200 и выше (раунд 5).
func TestDesignLoadBranchReadsInBoundedChunks(t *testing.T) {
	require.Less(t, DesignBranchChunk, 200, "с 200 равенств в IN оценка идёт по статистике, а не погружениями")
	pieces := 2*DesignBranchChunk + 1
	nodes := []DesignBranchNode{shownNode(standingSheet)}
	var editIDs []int
	for i := 0; i < pieces; i++ {
		piece, edit := 10+2*i, 11+2*i
		nodes = append(nodes,
			replacedBy(cutFrom(hiddenNode(piece), standingSheet), edit),
			editOf(hiddenNode(edit), piece))
		editIDs = append(editIDs, edit)
	}
	table := newBranchTable(nodes...)
	loaded, err := DesignLoadBranch(nodes[0], table.reads())
	require.NoError(t, err)
	require.Len(t, loaded, 1+2*pieces)

	var sizes []string
	var replaced []int
	for _, c := range table.calls {
		require.LessOrEqual(t, len(c.ids), DesignBranchChunk, "%s: %d id в одном запросе", c.read, len(c.ids))
		require.True(t, sort.IntsAreSorted(c.ids))
		sizes = append(sizes, fmt.Sprintf("%s:%d", c.read, len(c.ids)))
		if c.read == "id" {
			replaced = append(replaced, c.ids...)
		}
	}
	full := func(read string) []string {
		return []string{
			fmt.Sprintf("%s:%d", read, DesignBranchChunk), fmt.Sprintf("%s:%d", read, DesignBranchChunk), read + ":1",
		}
	}
	want := []string{"crops:1"}
	want = append(want, full("id")...)
	want = append(want, full("crops")...)
	want = append(want, full("crops")...)
	require.Equal(t, want, sizes)
	require.Equal(t, editIDs, replaced, "цели замен прочитаны все, по разу")

	got, err := DesignStandingPieces(standingSheet, loaded)
	require.NoError(t, err)
	require.Empty(t, got)
}

// ОДИН ПОТОЛОК НА ВСЁ ЧТЕНИЕ, ЛИСТ ВКЛЮЧИТЕЛЬНО, И ОТКАЗ — НА ТОМ ЖЕ ШАГЕ.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: считать потолок на уровень (линия из одного кадра на уровень не упирается
// ни в один уровень и дочитывается до конца); не сверять ответ с оставшимся местом (лишнее
// принимается); сдвиг границы на единицу («ровно потолок» отказывает либо «на один больше» проходит);
// читать дальше отказа (широкий уровень делает больше одного вызова).
func TestDesignLoadBranchSharesOneCap(t *testing.T) {
	sheet := shownNode(standingSheet)
	t.Run("ровно потолок", func(t *testing.T) {
		nodes := append([]DesignBranchNode{sheet}, standingLine(10, DesignStandingNodesMax-1)...)
		loaded, err := DesignLoadBranch(sheet, newBranchTable(nodes...).reads())
		require.NoError(t, err)
		require.Len(t, loaded, DesignStandingNodesMax)
	})
	t.Run("на один кадр больше — по кадру на уровень", func(t *testing.T) {
		nodes := append([]DesignBranchNode{sheet}, standingLine(10, DesignStandingNodesMax)...)
		table := newBranchTable(nodes...)
		_, err := DesignLoadBranch(sheet, table.reads())
		require.Error(t, err)
		require.Contains(t, err.Error(), fmt.Sprintf("more than %d pictures", DesignStandingNodesMax))
		require.NotErrorIs(t, err, ErrDesignCutSheet)
		last := table.calls[len(table.calls)-1]
		require.Equal(t, 1, last.limit, "последнее чтение просило одну строку сверх потолка — и получило её")
	})
	t.Run("широкий уровень над потолком — один вызов и отказ", func(t *testing.T) {
		nodes := []DesignBranchNode{sheet}
		for i := 0; i < DesignStandingNodesMax; i++ {
			nodes = append(nodes, cutFrom(hiddenNode(10+i), standingSheet))
		}
		table := newBranchTable(nodes...)
		_, err := DesignLoadBranch(sheet, table.reads())
		require.Error(t, err)
		require.Len(t, table.calls, 1, "дальше отказа не читается")
		require.Equal(t, DesignStandingNodesMax, table.calls[0].limit, "место под потолком плюс одна строка")
	})
}

// ЗАМЕНА САМОГО ЛИСТА НЕ ЧИТАЕТСЯ: в правило она не входит, а заменённый лист отказан раньше.
//
// МУТАЦИЯ: начинать уровни с листа как с обычного кадра — лишнее чтение 50 и её ветки под замком.
func TestDesignLoadBranchSkipsTheSheetsOwnReplacement(t *testing.T) {
	sheet := replacedBy(shownNode(standingSheet), 50)
	table := newBranchTable(sheet, editOf(shownNode(50), standingSheet), cutFrom(hiddenNode(10), standingSheet))
	loaded, err := DesignLoadBranch(sheet, table.reads())
	require.NoError(t, err)
	require.Equal(t, []int{7, 10}, loadedIDs(loaded))
	for _, c := range table.calls {
		require.NotContains(t, c.ids, 50)
	}
}

// ОШИБКА ЧТЕНИЯ ВИДНА ПОВТОРУ ТРАНЗАКЦИИ.
//
// МУТАЦИЯ: завернуть ошибку чтения без %w — дедлок 1213 перестал бы повторяться.
func TestDesignLoadBranchKeepsTheReadError(t *testing.T) {
	transient := errors.New("Error 1213: Deadlock found when trying to get lock")
	table := newBranchTable(shownNode(standingSheet))
	table.fail = transient
	_, err := DesignLoadBranch(shownNode(standingSheet), table.reads())
	require.ErrorIs(t, err, transient)
	require.Contains(t, err.Error(), "design picture 7")
}

// ─── ПОВТОР ПО КЛЮЧУ ОТВЕЧАЕТ ТОЛЬКО ТОМУ ЖЕ ЖЕСТУ (0370/0371, O-53 review) ───

// replayPrior — кадр, поданный первой попыткой: флэттен слоя 5 на ревизии 4.
func replayPrior() DesignPicture {
	return DesignPicture{
		Id: 12, TechCardId: replaceProbeCard, Derivation: DesignDerivationFlatten,
		DerivedFrom:   sql.NullInt32{Int32: 7, Valid: true},
		SourceLayerId: sql.NullInt32{Int32: 5, Valid: true}, LayerRev: 4,
	}
}

func replayReq(layer, rev, replace int) DesignEditLayerFlatten {
	return DesignEditLayerFlatten{
		TechCardId: replaceProbeCard, LayerId: layer, ExpectedRev: rev, MediaId: 901,
		ReplacePictureId: replace, ClientRequestId: "k-1",
	}
}

// ПОВТОР ТОГО ЖЕ ЖЕСТА ПРОХОДИТ — В ОБОИХ РЕЖИМАХ И У СЛОЯ С ЧИСТОГО ЛИСТА.
//
// Положительный контроль: без него пробы отказов ниже зеленели бы и на правиле, отказывающем всегда.
// media_id запроса намеренно не совпадает ни с чем — ключ это жест, а не байты.
func TestDesignFlattenReplayOfTheSameGestureIsAReplay(t *testing.T) {
	require.NoError(t, DesignFlattenReplayRefusal(replayReq(5, 4, 7), "k-1", replayPrior(), 7), "перезапись")
	require.NoError(t, DesignFlattenReplayRefusal(replayReq(5, 4, 0), "k-1", replayPrior(), 0), "рядом")

	root := replayPrior()
	root.DerivedFrom, root.Derivation = sql.NullInt32{}, DesignDerivationNone
	require.NoError(t, DesignFlattenReplayRefusal(replayReq(5, 4, 0), "k-1", root, 0),
		"слой с чистого листа: у кадра нет родителя, есть слой")
}

// КЛЮЧ, ПОТРАЧЕННЫЙ НА ДРУГОЙ ФЛЭТТЕН, — invalid_argument, И КАЖДОЕ РАСХОЖДЕНИЕ НАЗЫВАЕТ СЕБЯ.
//
// Главный случай раунда 2 — ДРУГОЙ СЛОЙ НА ТОЙ ЖЕ РЕВИЗИИ, в обоих режимах: раньше повтор «save as
// new» слоя L2 под ключом слоя L1 получал картинку L1 как свой успех.
//
// МУТАЦИИ: снять сверку слоя (оба подслучая «другой слой» проходят); читать отсутствие слоя как
// совпадение (кадр до 0371 отвечает любому слою); снять сверку ревизии, глагола или места.
func TestDesignFlattenReplayRefusesAKeySpentElsewhere(t *testing.T) {
	noLayer := replayPrior()
	noLayer.SourceLayerId = sql.NullInt32{}
	crop := replayPrior()
	crop.Derivation = DesignDerivationCrop

	for _, tc := range []struct {
		name  string
		req   DesignEditLayerFlatten
		prior DesignPicture
		took  int
		says  string
	}{
		{"другой слой, та же ревизия — рядом", replayReq(6, 4, 0), replayPrior(), 0, "from layer 5, not layer 6"},
		{"другой слой, та же ревизия — перезапись", replayReq(6, 4, 7), replayPrior(), 7, "from layer 5, not layer 6"},
		{"кадр без записанного слоя", replayReq(5, 4, 0), noLayer, 0, "records no layer"},
		{"кусок разреза", replayReq(5, 4, 0), crop, 0, "is a crop"},
		{"другая ревизия", replayReq(5, 5, 0), replayPrior(), 0, "layer rev 4, not 5"},
		{"подан рядом, просят на место", replayReq(5, 4, 7), replayPrior(), 0, "filed beside"},
		{"подан на место, просят рядом", replayReq(5, 4, 0), replayPrior(), 7, "this request files beside"},
		{"подан на место другого кадра", replayReq(5, 4, 8), replayPrior(), 7, "not of picture 8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := DesignFlattenReplayRefusal(tc.req, "k-1", tc.prior, tc.took)
			require.ErrorIs(t, err, ErrDesignInvalidArgument)
			require.Contains(t, err.Error(), tc.says)
			require.Contains(t, err.Error(), `client_request_id "k-1" already filed picture 12`)
		})
	}
}
