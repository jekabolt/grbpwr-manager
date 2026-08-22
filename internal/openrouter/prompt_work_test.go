package openrouter

import (
	"strings"
	"testing"
)

// ОСЬ «РАБОТА» В ПРОМПТЕ: КАТАЛОГ ЕДЕТ ЦЕХОВЫМИ СЛОВАМИ, А НЕ ОДНИМИ ТОКЕНАМИ.
//
// МУТАЦИЯ, ПРОГНАННАЯ ПО ЭТОМУ ФАЙЛУ (ставилась одна, прогонялась, откатывалась): из
// writeWorkCatalog убран блок синонимов («words: …»). ПОКРАСНЕЛ
// TestUserPromptCarriesTheWorkCatalogInTheWordsOfTheShopFloor — русское слово исчезло из промпта,
// то есть модели, читающей русскую диктовку, снова пришлось бы переводить и угадывать токен по
// английскому ярлыку. Проба держит именно это, а не факт наличия списка.

// workCatalogSample — три работы сида 0329/0331, покрывающие все три режима вопроса «на чём».
func workCatalogSample() []WorkContext {
	return []WorkContext{
		{
			Token: "moscow_hem", Label: "Hem — rolled (Moscow)", Verb: "machine",
			Machines: []string{"lockstitch"},
			Syn:      []string{"московский", "московский шов", "узкая подгибка", "moscow hem"},
		},
		{
			Token: "slit_overcast", Label: "Slit — overcast", Verb: "machine",
			Machines: []string{"zigzag", "buttonhole"},
			Syn:      []string{"прорезь", "обметать прорезь", "slit overcast"},
		},
		{
			Token: "press_flat", Label: "Press flat", Verb: "press",
			Syn: []string{"приутюжить", "press flat"},
		},
	}
}

// TestUserPromptCarriesTheWorkCatalogInTheWordsOfTheShopFloor — ЦИТАТА (г).
//
// Вход этой функции — РЕЧЬ ТЕХНОЛОГА, надиктованная по-русски, а ярлыки каталога английские.
// Токен без цеховых слов заставляет модель переводить и угадывать, а угаданный токен роняется на
// приёме — то есть шаг возвращается без работы, ровно как до фазы. Поэтому проба требует ОБОИХ:
// токена, которым отвечают, и русского слова, по которому его находят.
func TestUserPromptCarriesTheWorkCatalogInTheWordsOfTheShopFloor(t *testing.T) {
	tcx := sampleContext()
	tcx.Works = workCatalogSample()
	p := buildUserPrompt(tcx, "подогнуть низ московским, прорезь обметать")

	for _, want := range []string{
		"WORK CATALOG", // список назван, и назван тем же словом, что и правило системного промпта
		"moscow_hem",   // токен, которым отвечают
		"московский",   // слово, которым технолог его называет — без него токен не найти
		"обметать прорезь",
		"приутюжить",
		"Hem — rolled (Moscow)", // ярлык: он же поедет на печатный лист
		"on: zigzag / buttonhole",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт не несёт %q:\n%s", want, p)
		}
	}

	// Работа НЕСЁТ глагол, и правило когерентности 0330 отвергает шаг, чей глагол ей не равен.
	// Модель, не видящая глагола работы, отвечает несогласованно — и работа теряется на приёме.
	if !strings.Contains(p, "moscow_hem | Hem — rolled (Moscow) | machine") {
		t.Errorf("строка каталога не называет глагол работы:\n%s", p)
	}

	// Каталога нет — промпт о работах МОЛЧИТ. Спросить токен, не показав списка, значит попросить
	// выдумать его; это тот же выбор, которым файл отвечает на незаданный дефолт карточки.
	if silent := buildUserPrompt(sampleContext(), "sew it"); strings.Contains(silent, "WORK CATALOG") {
		t.Errorf("пустой каталог всё равно попросил работу:\n%s", silent)
	}
}

// Системный промпт формулирует правило прямо: токен или молчание. «Выдуманный токен хуже пустого
// поля» — не стилистика, а описание приёма: несуществующий токен роняется, а прочитавший его
// технолог пошёл бы искать работу, которой нет.
func TestSystemPromptAsksForACatalogTokenOrSilence(t *testing.T) {
	for _, want := range []string{
		`"work"`,
		"WORK CATALOG",
		"NAME THE WORK WITH A TOKEN FROM THAT LIST, OR SAY NOTHING AT ALL",
		"never bend a token into one that",
		"THE WORK CARRIES THE VERB AND THE MACHINE",
	} {
		if !strings.Contains(systemPrompt, want) {
			t.Errorf("системный промпт не несёт %q", want)
		}
	}
}
