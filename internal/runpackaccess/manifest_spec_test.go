package runpackaccess

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/cutspec"
)

// readManifest — общий пролог: минт токена, запрос, разбор документа.
func readManifest(t *testing.T, svc *Service, runID int) Manifest {
	t.Helper()
	token := svc.MintRunPackToken(context.Background(), runID)
	w := serveRunPack(svc, http.MethodGet, "/api/rp/"+token)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	var m Manifest
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest is not json: %v", err)
	}
	return m
}

// TestManifestCutsByReleaseSnapshot — ГЛАВНЫЙ тест наряда, привязанного к релизу. Прогон #41 несёт
// release_id 12 (Rev.3), и снапшот этого релиза описывает СОВСЕМ ДРУГУЮ спецификацию, чем живая
// карта: другая деталь, две панели вместо одной, другие артикулы. Наряд обязан напечатать релизную.
//
// Дефект, ради которого тест написан, выглядел ровно так: шапка говорила «Rev.3», а детали и ткани
// приезжали из живой карты — то есть бумага утверждала, что кроят по утверждённой ревизии, и
// одновременно велела кроить по неутверждённой.
func TestManifestCutsByReleaseSnapshot(t *testing.T) {
	svc, _, _ := newTestService(t)
	m := readManifest(t, svc, 41)

	if m.SpecSource != string(cutspec.SourceReleaseSnapshot) {
		t.Fatalf("spec_source = %q, want %q", m.SpecSource, cutspec.SourceReleaseSnapshot)
	}
	if m.ReleaseId != 12 || m.ReleaseNumber != 3 {
		t.Fatalf("release = %d/%d, want 12/3", m.ReleaseId, m.ReleaseNumber)
	}
	if len(m.CutList) != 2 {
		t.Fatalf("cut list = %+v, want две строки (два колорвея одной детали)", m.CutList)
	}
	for _, row := range m.CutList {
		if row.PieceName != relPieceName {
			t.Fatalf("наряд напечатал деталь %q — спецификация взята НЕ из снапшота релиза: %+v",
				row.PieceName, row)
		}
		if row.PiecesPerGarment != 2 {
			t.Fatalf("pieces_per_garment = %d, want 2 (в снапшоте деталь кроится дважды): %+v",
				row.PiecesPerGarment, row)
		}
	}
	// Каталожные имена артикулов дочитаны: без этого в колонке артикула стояла бы РОЛЬ слота
	// («основная ткань (роль)») у обоих колорвеев, и пин был бы неотличим от умолчания.
	black, blue := m.CutList[0], m.CutList[1]
	if black.ColorwayId != 55 || black.MaterialId != 200 || black.MaterialName != "ткань релиза" || black.Pinned {
		t.Fatalf("строка чёрного = %+v, want артикул 200 «ткань релиза» без пина", black)
	}
	if blue.ColorwayId != 66 || blue.MaterialId != 201 || blue.MaterialName != "подкладка релиза" || !blue.Pinned {
		t.Fatalf("строка синего = %+v, want ПИН на артикул 201 «подкладка релиза»", blue)
	}
	// 26 изделий чёрного (20+6) + 10 xs, всё ×2 панели; синий: 5 + 3 вне градации, ×2.
	if black.PiecesToCutTotal != 72 || blue.PiecesToCutTotal != 16 {
		t.Fatalf("панелей к раскрою: чёрный %d, синий %d — want 72 и 16 (garments × 2)",
			black.PiecesToCutTotal, blue.PiecesToCutTotal)
	}
	if m.PiecesToCutTotal != 88 {
		t.Fatalf("pieces_to_cut_total = %d, want 88", m.PiecesToCutTotal)
	}
	// Ни одной живой детали в документе: если бы спецификацию взяли из живой карты, здесь было бы
	// её имя — и именно так дефект выглядел на бумаге.
	if strings.Contains(strings.ToLower(dump(t, m)), livePieceCut) {
		t.Fatalf("в наряде по релизу засветилась ЖИВАЯ деталь %q:\n%s", livePieceCut, dump(t, m))
	}
}

// TestUnreadableSnapshotDegradesWithoutLying — снапшот, который не читается текущей схемой. Наряд
// обязан деградировать на живую карту (пустой экран у закройщика хуже), но НЕ ИМЕЕТ ПРАВА при этом
// утверждать, что посчитан по Rev.3: вьюер печатает «Rev.N» из release_number, и заполненный номер
// над числами живой карты — это ровно та бумага, из-за которой кроят не то.
func TestUnreadableSnapshotDegradesWithoutLying(t *testing.T) {
	svc, _, cards := newTestService(t)
	cards.release.Snapshot = "{ это не proto-json"

	m := readManifest(t, svc, 41)

	if m.ReleaseId != 0 || m.ReleaseNumber != 0 {
		t.Fatalf("release = %d/%d — посчитано по живой карте, а шапка называет ревизию",
			m.ReleaseId, m.ReleaseNumber)
	}
	if m.SpecSource != string(cutspec.SourceLiveCardFallback) {
		t.Fatalf("spec_source = %q, want %q — «живая карта» и «релиз не прочитался» это разные вещи",
			m.SpecSource, cutspec.SourceLiveCardFallback)
	}
	if len(m.CutList) == 0 || m.CutList[0].PieceName != livePieceCut {
		t.Fatalf("деградация обязана печатать ЖИВУЮ карту: %+v", m.CutList)
	}
	// Про деградацию сказано словами и первой оговоркой: цех читает шапку и оговорки, а не поля.
	if len(m.CutCaveats) == 0 || !strings.Contains(m.CutCaveats[0], "Rev.3") {
		t.Fatalf("оговорка о деградации обязана назвать ревизию, по которой НЕ посчитано: %+v", m.CutCaveats)
	}
}

// TestEmptySnapshotDegrades — пустой блоб (релиз, снятый до того, как снапшоты вообще писали) ведёт
// себя как нечитаемый: живая карта плюс честный признак, а не отказ.
func TestEmptySnapshotDegrades(t *testing.T) {
	svc, _, cards := newTestService(t)
	cards.release.Snapshot = "{}"

	m := readManifest(t, svc, 41)
	if m.ReleaseNumber != 0 || m.SpecSource != string(cutspec.SourceLiveCardFallback) {
		t.Fatalf("пустой снапшот: release=%d spec_source=%q", m.ReleaseNumber, m.SpecSource)
	}
	if len(m.CutList) == 0 || m.CutList[0].PieceName != livePieceCut {
		t.Fatalf("пустой снапшот обязан деградировать на живую карту: %+v", m.CutList)
	}
}

// TestReleaseOfAnotherCardIsRefused — прогон ссылается на релиз ЧУЖОЙ карточки. Составного ключа
// между production_run.release_id и tech_card_id в схеме нет, так что это состояние достижимо; и
// это единственный случай, когда «посчитать по релизу» означает напечатать детали другого стиля.
func TestReleaseOfAnotherCardIsRefused(t *testing.T) {
	svc, _, cards := newTestService(t)
	cards.release.TechCardId = 999

	m := readManifest(t, svc, 41)
	if m.ReleaseId != 0 || m.SpecSource != string(cutspec.SourceLiveCardFallback) {
		t.Fatalf("чужой релиз: release_id=%d spec_source=%q — по нему кроить нельзя", m.ReleaseId, m.SpecSource)
	}
	if len(m.CutList) == 0 || m.CutList[0].PieceName != livePieceCut {
		t.Fatalf("чужой релиз обязан деградировать на живую карту этого прогона: %+v", m.CutList)
	}
}

// TestReleaseReadFailureIsNotADegradation — таймаут базы на чтении релиза НЕ имеет права стать
// нарядом по живой карте. Сбой чтения проходит и возвращается; наряд, выданный в цех по
// неутверждённой спецификации, — нет. Ответ тот же голый 404, что и у всех отказов этого эндпоинта.
func TestReleaseReadFailureIsNotADegradation(t *testing.T) {
	svc, _, cards := newTestService(t)
	cards.releaseErr = errors.New("dial tcp: i/o timeout")

	token := svc.MintRunPackToken(context.Background(), 41)
	if w := serveRunPack(svc, http.MethodGet, "/api/rp/"+token); w.Code != http.StatusNotFound {
		t.Fatalf("сбой чтения релиза: got %d, want 404 (документа нет — лучше, чем чужая спецификация)", w.Code)
	}
}

// TestRunWithoutReleaseSaysLiveCard — прогон без релиза: живая карта — это НОРМА, а не авария, и
// spec_source обязан отличать её от деградации, иначе вьюер повесит тревожный значок на каждый
// обычный наряд.
func TestRunWithoutReleaseSaysLiveCard(t *testing.T) {
	svc, runs, cards := newTestService(t)
	runs.pack.Run.ReleaseId = ni64(0)
	runs.pack.Run.ReleaseId.Valid = false
	runs.pack.ReleaseNumber = 0

	m := readManifest(t, svc, 41)
	if m.SpecSource != string(cutspec.SourceLiveCard) || m.ReleaseNumber != 0 {
		t.Fatalf("прогон без релиза: spec_source=%q release=%d", m.SpecSource, m.ReleaseNumber)
	}
	if len(m.CutCaveats) > 0 && strings.Contains(m.CutCaveats[0], "релиз") {
		t.Fatalf("прогон без релиза не должен жаловаться на релиз: %+v", m.CutCaveats)
	}
	// Живая карта читается ОДИН раз: второе чтение под материальный план нужно только тогда, когда
	// кат-лист посчитан по снапшоту.
	if cards.liveReads != 1 {
		t.Fatalf("live card reads = %d, want 1", cards.liveReads)
	}
}

func dump(t *testing.T, m Manifest) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return string(b)
}
