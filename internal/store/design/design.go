// Package design implements the DESIGN band store: generation runs and their paid attempts,
// the pictures they produce, the bench that promotes a picture into a view, the frozen sheet
// versions that get printed, the vector edit layers and the day's money.
//
// TRANSACTIONS. Every write here runs inside store.Tx, which is already SERIALIZABLE
// (internal/store/db.go:63, :165); reads run inside store.readTx, REPEATABLE READ read-only
// (:69); transient 1213/1205 are retried by the caller (:107-141). "Read, check, write" inside
// one Tx is therefore honest without hand-rolled locking.
//
// BUT ISOLATION IS NOT IDEMPOTENCY, and it cannot tell a stale intention from a fresh one.
// SERIALIZABLE closes the write race; it does not know that the person was looking at a screen
// from four minutes ago. That is the job of client_request_id (UNIQUE — a repeat returns the
// existing row with OK instead of filing a second one), of slot_rev and of expected_rev. None of
// the three is redundant with the isolation level and none may be dropped as an "optimisation".
//
// THE TRANSACTION CALLBACK GETS THE WHOLE REPOSITORY (db.go:62), so rep.TechCards() and
// rep.Design() live in the SAME transaction. Wave 2's atomic mint depends on exactly that
// property — do not narrow the callback to a bare DB handle.
package design

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// TxFunc runs f inside a transaction, handing it the full repository.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.Design.
type Store struct {
	storeutil.Base
	txFunc     TxFunc
	readTxFunc TxFunc
}

// New builds the design band store. readTxFunc is separate and load-bearing: GetBand runs its
// page AND its aggregates in one REPEATABLE READ snapshot, and a counter taken outside that
// snapshot would disagree with the page it captions.
func New(base storeutil.Base, txFunc, readTxFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc, readTxFunc: readTxFunc}
}

// Ceilings declared by the contract (10 §6). They are enforced here rather than only at the
// handler because the store is the last place that can still refuse.
const (
	// MaxRunPageLimit / DefaultRunPageLimit — history page. Four rows on a screen, three
	// screens of slack; larger is a picture flood, not a page.
	MaxRunPageLimit     = 24
	DefaultRunPageLimit = 12
	// MaxStrokesBytes — one vector edit. Beyond it the answer is "too many strokes, split it",
	// not a slower save.
	MaxStrokesBytes = 512 * 1024
	// MaxBandBatches — upload shelves shipped by one band read, newest first, each WITH its full
	// picture list. Twenty-four is two screens of shelves, the same order of magnitude as the run
	// page and for the same reason: a shelf is read at a glance, and a card that has more of them
	// than that is a card whose upload history wants its own paged read — which does not exist
	// yet. What happens after the ceiling: those batches are not shipped and carry no badge,
	// which is honest, because they are not on the screen either. TotalBatches makes the overflow
	// measurable rather than invisible.
	MaxBandBatches = 24
	// colourRecipeScanRuns / MaxColourRecipes — the colour-history chips. Scanned over the
	// card's render runs newest-first (never over the loaded page), de-duplicated by the recipe
	// text, and capped. The plan says "the last N render runs" without naming N; N is fixed here
	// and stated out loud so two readers cannot pick two different numbers.
	colourRecipeScanRuns = 24
	MaxColourRecipes     = 12
	// MaxCardOutputsPerColorway — потолок ГЕНЕРАТИВНЫХ ВЫХОДОВ, которые полоса везёт НА ОДИН
	// КОЛОРВЕЙ (DesignBand.Outputs). Шестьдесят.
	//
	// ⚠ ЗДЕСЬ СТОЯЛ ДОВОД «ЭТИ РОДЫ ПЛАТНЫЕ, ЗНАЧИТ ИХ ЧИСЛО ОГРАНИЧЕНО ДЕНЬГАМИ», И ОН БЫЛ ЛОЖЕН.
	// Строку выхода рождает не только оплаченный прогон. SplitPicture пишет кропы, ЯВНО говоря
	// «никаких денег на разрез не потрачено» (pictures.go), и наследует у родителя И run_id, И
	// kind — то есть каждый кроп листа рендера это ещё одна строка ЭТОГО ЖЕ предиката. FlattenLayer
	// делает то же самое (layer.go). Ни то, ни другое не дедуплицируется, а предикат намеренно не
	// фильтрует hidden_at, поэтому «спрятать кропы и нарезать заново» добавляет строки НАВСЕГДА.
	// Замерено на одноразовом контейнере: ОДИН оплаченный рендер-прогон плюс три бесплатных цикла
	// «спрятать/перерезать» дают 16 выходов; ещё дюжина циклов — 200. Потолок достижим бесплатно.
	//
	// ЧЕМ ЖЕ ОН ОГРАНИЧЕН НА САМОМ ДЕЛЕ. Не деньгами, а ОТВЕТОМ: каждый выход везёт MediaFull, и
	// шапка GetDesignBandResponse говорит про полосу ровно это — «карточка с 40 прогонами по 3
	// выхода отгружала бы 120 MediaFull на каждое открытие вкладки». То есть это потолок ПАМЯТИ, и
	// раз он достижим, важно не его величина, а КАК он тратится.
	//
	// ПОЭТОМУ ОН ПОКОЛОРВЕЙНЫЙ, А НЕ ОБЩИЙ. Общий потолок в 200 с `ORDER BY p.id DESC` выбрасывал
	// САМЫЕ СТАРЫЕ строки — то есть на карточке с шестью колорвеями раздел колорвея F мог опустеть
	// целиком, потому что все его кадры старше двухсотого id. Это ровно тот дефект, ради которого
	// волна H-9 и существует, просто отложенный до горизонта в 200 строк. Поколорвейное окно
	// (ROW_NUMBER PARTITION BY колорвею кадра) делает голодание невыразимым: ни один раздел не
	// может быть съеден объёмом соседнего.
	//
	// ЧЕМ ОГРАНИЧЕН ОТВЕТ ЦЕЛИКОМ: (число колорвеев карточки + 1) × 60. Это осознанный размен, а
	// не недосмотр. Колорвей — единственная ось на этой карточке, которая растёт ТОЛЬКО от
	// человеческого решения (это строка product, её кто-то завёл), тогда как кропы и флэттены
	// множатся даром. Полоса уже отгружает по этой же оси неограниченный список MediaFull —
	// верстак (listBenchSlots, LIMIT нет вовсе), — так что выходы теперь ограничены не хуже, а
	// строго лучше своего соседа.
	//
	// ЧТО ПРОИСХОДИТ ПОСЛЕ ПОТОЛКА: у КАЖДОГО колорвея везутся 60 САМЫХ СВЕЖИХ, а
	// DesignBand.OutputsTotalByColorway называет настоящее число ИМЕННО ТОГО раздела, который
	// читатель сузил. Общий OutputsTotal на такой вопрос не отвечает вовсе: «больше 200 где-то на
	// карточке» нечем подписать усечённый раздел.
	MaxCardOutputsPerColorway = 60
)

// mysqlDupKey reports whether err is 1062 and, when it is, which UNIQUE key it collided on.
// The key NAME matters: on design_bench_slot two different UNIQUE keys can raise 1062 and they
// mean opposite things — uq_design_bench_view is "somebody else touched this slot", while
// uq_design_bench_picture is "this plate already stands in another slot". Collapsing them into
// one refusal would tell a person to reload when the real answer is "that plate is taken".
func mysqlDupKey(err error) (string, bool) {
	var me *mysql.MySQLError
	if !errors.As(err, &me) || me.Number != 1062 {
		return "", false
	}
	// MySQL says: Duplicate entry 'x-y' for key 'design_bench_slot.uq_design_bench_picture'.
	const marker = "for key '"
	i := strings.LastIndex(me.Message, marker)
	if i < 0 {
		return "", true
	}
	rest := me.Message[i+len(marker):]
	j := strings.IndexByte(rest, '\'')
	if j < 0 {
		return "", true
	}
	key := rest[:j]
	if k := strings.LastIndexByte(key, '.'); k >= 0 {
		key = key[k+1:]
	}
	return key, true
}

// isDupKey reports a plain 1062 without caring which key.
func isDupKey(err error) bool {
	_, ok := mysqlDupKey(err)
	return ok
}

// nullStr / nullInt / jsonOrNil are the three shapes every insert in this package needs.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(i int) any {
	if i == 0 {
		return nil
	}
	return i
}

func jsonOrNil(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

// requireCard refuses early when a caller addresses no card at all. A zero id would otherwise
// read as "the card whose id is zero" and quietly return an empty band.
func requireCard(id int) error {
	if id <= 0 {
		return fmt.Errorf("%w: tech card id is required", entity.ErrDesignInvalidArgument)
	}
	return nil
}

// loadCardPictures resolves the pictures of a set of runs (or of a batch) in ONE query, never
// per row. It is the only place picture rows are read for a list.
func loadPicturesByRuns(ctx context.Context, db dependency.DB, runIDs []int) (map[int][]entity.DesignPicture, error) {
	out := map[int][]entity.DesignPicture{}
	if len(runIDs) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[entity.DesignPicture](ctx, db, `
		SELECT * FROM design_picture WHERE run_id IN (:ids) ORDER BY run_id, ordinal, id`,
		map[string]any{"ids": runIDs})
	if err != nil {
		return nil, fmt.Errorf("failed to load design pictures of runs: %w", err)
	}
	for _, p := range rows {
		if !p.RunId.Valid {
			continue
		}
		rid := int(p.RunId.Int32)
		out[rid] = append(out[rid], p)
	}
	return out, nil
}

// resolveMedia fills the Media pointer of every picture in place, in ONE batch read, inside the
// caller's transaction. A missing media row leaves Media nil rather than dropping the picture:
// "the file disappeared" is a fact the band must be able to show, not a row to hide.
func resolveMedia(ctx context.Context, rep dependency.Repository, pics []*entity.DesignPicture) error {
	ids := make([]int, 0, len(pics))
	seen := map[int]struct{}{}
	for _, p := range pics {
		if p == nil || p.MediaId == 0 {
			continue
		}
		if _, ok := seen[p.MediaId]; ok {
			continue
		}
		seen[p.MediaId] = struct{}{}
		ids = append(ids, p.MediaId)
	}
	if len(ids) == 0 {
		return nil
	}
	byID, err := rep.Media().GetMediaByIds(ctx, ids)
	if err != nil {
		return fmt.Errorf("failed to resolve design picture media: %w", err)
	}
	for _, p := range pics {
		if p == nil {
			continue
		}
		if m, ok := byID[p.MediaId]; ok {
			mm := m
			p.Media = &mm
		}
	}
	return nil
}

// resolveMediaInto is resolveMedia for anything that holds a media id and a *MediaFull, which
// sheet plates and frozen callouts do as well as pictures.
func resolveMediaIDs(ctx context.Context, rep dependency.Repository, ids []int) (map[int]entity.MediaFull, error) {
	if len(ids) == 0 {
		return map[int]entity.MediaFull{}, nil
	}
	uniq := make([]int, 0, len(ids))
	seen := map[int]struct{}{}
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return map[int]entity.MediaFull{}, nil
	}
	return rep.Media().GetMediaByIds(ctx, uniq)
}

// pictureByID reads one picture row inside the caller's transaction.
func pictureByID(ctx context.Context, db dependency.DB, id int) (entity.DesignPicture, error) {
	p, err := storeutil.QueryNamedOne[entity.DesignPicture](ctx, db,
		`SELECT * FROM design_picture WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return p, fmt.Errorf("%w: design picture %d", entity.ErrDesignNotFound, id)
		}
		return p, fmt.Errorf("failed to load design picture %d: %w", id, err)
	}
	return p, nil
}

// OrphanedMedia reports which of the media a caller MINTED were not adopted by the rows the store
// filed. It lives here, next to the split, because it encodes a decision about the band rather
// than about transport.
//
// ⚠ THE CASE THAT MAKES IT NECESSARY IS err == nil. A split whose transaction short-circuits
// idempotently returns the crops of an EARLIER cut, so this call's freshly uploaded files were
// adopted by nothing at all — and a compensation that only ran on error would leave them in the
// bucket and in `media` forever, publicly addressable and owned by nobody. "It returned no error"
// is not "what I uploaded was taken".
func OrphanedMedia(minted []int, adopted []int) []int {
	if len(minted) == 0 {
		return nil
	}
	taken := make(map[int]struct{}, len(adopted))
	for _, id := range adopted {
		taken[id] = struct{}{}
	}
	var out []int
	for _, id := range minted {
		if id == 0 {
			continue
		}
		if _, ok := taken[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}
