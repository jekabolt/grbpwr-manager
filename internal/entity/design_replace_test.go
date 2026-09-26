package entity

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// ПРОБЫ ПРАВИЛА «ПЕРЕЗАПИСАТЬ» (0368, O-53) — решения без базы.
//
// Чтения (кадр, число его видимых кропов) делает стор в транзакции флэттена, и они проверяются
// живыми пробами internal/store/design/replace_db_test.go (одноразовый контейнер, CI=1). Здесь —
// то, что от базы не зависит: какой отказ звучит при каком состоянии и В КАКОМ ПОРЯДКЕ.

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
// Сюда же — кропы, которые в счёт не идут: вызывающий считает только видимые (заменённые своей
// правкой — тоже, O-53 review), и ноль — это «резать нечего», а не «не спросили».
func TestDesignReplaceRefusalLetsTheNamedOriginalThrough(t *testing.T) {
	require.NoError(t, DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), replaceProbeOriginal(), 0))

	// Кроп и правка кропа — законные оригиналы: «edit a piece instead» ведёт именно сюда.
	piece := replaceProbeOriginal()
	piece.DerivedFrom = sql.NullInt32{Int32: 3, Valid: true}
	piece.Derivation = DesignDerivationCrop
	require.NoError(t, DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), piece, 0))

	// Спрятанный оригинал не отказывается: прятанье — другой ярус, и правило о нём не судит.
	hidden := replaceProbeOriginal()
	hidden.HiddenAt = sql.NullTime{Valid: true}
	require.NoError(t, DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), hidden, 0))
}

// КАЖДЫЙ ОТКАЗ НАЗЫВАЕТ СВОЁ, И ТОЛЬКО СВОЁ.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: снять любую из пяти проверок (подслучай становится nil); перепутать
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
		crops int
		want  error
	}{
		{"кадр чужой карточки", replaceProbeCard + 1, replaceProbeBase(replaceProbeMedia),
			func(p DesignPicture) DesignPicture { return p }, 0, ErrDesignReplaceMismatch},
		{"слой нарисован с чистого листа", replaceProbeCard, sql.NullInt32{},
			func(p DesignPicture) DesignPicture { return p }, 0, ErrDesignReplaceMismatch},
		{"подложка слоя — ноль", replaceProbeCard, sql.NullInt32{Valid: true},
			func(p DesignPicture) DesignPicture { return p }, 0, ErrDesignReplaceMismatch},
		{"слой нарисован поверх другого файла", replaceProbeCard, replaceProbeBase(replaceProbeMedia + 1),
			func(p DesignPicture) DesignPicture { return p }, 0, ErrDesignReplaceMismatch},
		{"кадр уже заменён", replaceProbeCard, replaceProbeBase(replaceProbeMedia),
			func(p DesignPicture) DesignPicture {
				p.ReplacedBy = sql.NullInt32{Int32: 12, Valid: true}
				return p
			}, 0, ErrDesignAlreadyReplaced},
		{"заменённость читается по NULL, а не по знаку", replaceProbeCard, replaceProbeBase(replaceProbeMedia),
			func(p DesignPicture) DesignPicture {
				p.ReplacedBy = sql.NullInt32{Valid: true}
				return p
			}, 0, ErrDesignAlreadyReplaced},
		{"лист разрезан", replaceProbeCard, replaceProbeBase(replaceProbeMedia),
			func(p DesignPicture) DesignPicture { return p }, 2, ErrDesignCutSheet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := DesignReplaceRefusal(tc.card, tc.base, tc.pic(replaceProbeOriginal()), tc.crops)
			require.Error(t, err)
			require.ErrorIs(t, err, tc.want)
			for _, other := range []error{ErrDesignReplaceMismatch, ErrDesignAlreadyReplaced, ErrDesignCutSheet} {
				if !errors.Is(tc.want, other) {
					require.NotErrorIs(t, err, other, "один отказ — одна причина на проводе")
				}
			}
		})
	}
}

// ПОРЯДОК ОТКАЗОВ: от «чинится запросом» к «чинится другим жестом».
//
// Кадр, у которого неверно ВСЁ сразу, обязан получить replace_mismatch: запрос, назвавший не тот
// кадр, не должен выглядеть как «уже заменён» — клиент перечитал бы полосу и повторил ту же ошибку.
// Заменённый И разрезанный — already_replaced: слепой повтор перезаписи узнаёт себя по этому слову,
// даже если лист успели разрезать после первой подачи.
//
// МУТАЦИЯ: переставить проверки — одна из двух половин краснеет.
func TestDesignReplaceRefusalOrder(t *testing.T) {
	everythingWrong := replaceProbeOriginal()
	everythingWrong.TechCardId = replaceProbeCard + 1
	everythingWrong.MediaId = replaceProbeMedia + 1
	everythingWrong.ReplacedBy = sql.NullInt32{Int32: 12, Valid: true}
	require.ErrorIs(t,
		DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), everythingWrong, 3),
		ErrDesignReplaceMismatch)

	replacedAndCut := replaceProbeOriginal()
	replacedAndCut.ReplacedBy = sql.NullInt32{Int32: 12, Valid: true}
	require.ErrorIs(t,
		DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), replacedAndCut, 3),
		ErrDesignAlreadyReplaced)
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
// 1213 перестал бы повторяться транзакцией).
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

// ─── СТОРОЖ cut_sheet СУДИТ КУСОК ПО ГОЛОВЕ ВЕТКИ (O-53 review, раунд 2) ───

// cropBranch — кусок листа 7 и цепочка его замен: links[0] — сам кусок, дальше — правки по порядку,
// hidden[i] — спрятано ли i-е звено. Возвращает кусок и загрузчик по всем звеньям.
func cropBranch(firstID int, hidden ...bool) (DesignPicture, func(int) (DesignPicture, error)) {
	chain := map[int]DesignPicture{}
	for i, h := range hidden {
		p := DesignPicture{Id: firstID + i, TechCardId: replaceProbeCard}
		if i == 0 {
			p.DerivedFrom = sql.NullInt32{Int32: 7, Valid: true}
			p.Derivation = DesignDerivationCrop
		} else {
			p.DerivedFrom = sql.NullInt32{Int32: int32(firstID + i - 1), Valid: true}
			p.Derivation = DesignDerivationFlatten
		}
		if i+1 < len(hidden) {
			p.ReplacedBy = sql.NullInt32{Int32: int32(firstID + i + 1), Valid: true}
		}
		if h {
			p.HiddenAt = sql.NullTime{Valid: true}
		}
		chain[p.Id] = p
	}
	return chain[firstID], func(id int) (DesignPicture, error) {
		p, ok := chain[id]
		if !ok {
			return DesignPicture{}, fmt.Errorf("%w: design picture %d", ErrDesignNotFound, id)
		}
		return p, nil
	}
}

// КУСОК ДЕРЖИТ ЛИСТ, ПОКА НА ЭКРАНЕ ГОЛОВА ЕГО ВЕТКИ — ЧЕМ БЫ НИ БЫЛИ ЗВЕНЬЯ ДО НЕЁ.
//
// Сценарий ревью — устаревшая вкладка: кусок C спрятан, спрятанный C перезаписан, правка E видима —
// ветка держит лист. Обратный случай: кусок видим, но его правка спрятана — ветку сняли, лист
// свободен. Каждый случай судится ОТДЕЛЬНО: общий счёт скрыл бы мутацию «судить по строке куска»,
// при которой два неверных ответа складываются в верную сумму.
//
// МУТАЦИИ: судить по строке куска (два средних случая меняются местами); судить по первой правке, а
// не по голове (цепочка из двух правок со спрятанной первой); проглотить ошибку обхода нулём.
func TestDesignVisibleCropBranchesJudgesEachPieceByItsHead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		hidden []bool
		holds  bool
	}{
		{"кусок на виду, не перезаписан", []bool{false}, true},
		{"кусок спрятан, не перезаписан", []bool{true}, false},
		{"кусок спрятан, его правка на виду — устаревшая вкладка", []bool{true, false}, true},
		{"кусок на виду, его правка спрятана", []bool{false, true}, false},
		{"две правки: первая спрятана, голова на виду", []bool{false, true, false}, true},
		{"две правки: голова спрятана", []bool{true, false, true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			crop, load := cropBranch(10, tc.hidden...)
			n, err := DesignVisibleCropBranches([]DesignPicture{crop}, load)
			require.NoError(t, err)
			want := 0
			if tc.holds {
				want = 1
			}
			require.Equal(t, want, n)
		})
	}

	// Несколько кусков считаются вместе, а порча цепочки — ошибка, а не «куска нет».
	a, loadA := cropBranch(10, false)
	b, loadB := cropBranch(20, true, false)
	load := func(id int) (DesignPicture, error) {
		if id >= 20 {
			return loadB(id)
		}
		return loadA(id)
	}
	n, err := DesignVisibleCropBranches([]DesignPicture{a, b}, load)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	broken, _ := cropBranch(40, true, false)
	_, err = DesignVisibleCropBranches([]DesignPicture{broken}, func(int) (DesignPicture, error) {
		return DesignPicture{}, fmt.Errorf("%w: gone", ErrDesignNotFound)
	})
	require.Error(t, err, "не прочли ветку — не значит, что куска нет: лист не уходит под правку по порче")
}

// ─── ПОВТОР ПО КЛЮЧУ ОТВЕЧАЕТ ТОЛЬКО ТОМУ ЖЕ ЖЕСТУ (0369/0370, O-53 review) ───

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
// совпадение (кадр до 0370 отвечает любому слою); снять сверку ревизии, глагола или места.
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
