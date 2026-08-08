package productionrun

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Строки доступа к публичному наряду (production_run_pack_access, 0293) — прогонный близнец
// tech_card_pattern_viewer_access. Механика та же (ленивое создание, отзыв через epoch,
// отложенная статистика), различается только идентичность: ключ здесь — id ПРОГОНА, и достучаться
// до этих строк объектным или карточным токеном нельзя по построению (обработчики /api/p и /api/pv
// отказывают скоупу 'r' своим allowlist'ом, а /api/rp — всем остальным).

const runPackAccessColumns = `run_id, epoch, expires_at, revoked_at, last_access_at, access_count, created_at`

// GetRunPackAccess читает строку доступа одного прогона. sql.ErrNoRows, когда её нет.
func (s *Store) GetRunPackAccess(ctx context.Context, runID int) (*entity.ProductionRunPackAccess, error) {
	row, err := storeutil.QueryNamedOne[entity.ProductionRunPackAccess](ctx, s.DB,
		`SELECT `+runPackAccessColumns+` FROM production_run_pack_access WHERE run_id = :id`,
		map[string]any{"id": runID})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// EnsureRunPackAccess возвращает строку доступа прогона, создавая её лениво (epoch 1 из дефолта
// колонки, без срока). SELECT сначала — по той же причине, что и в EnsureCardViewer: этот вызов
// висит на каждом чтении прогона в админке, и в установившемся режиме за админским чтением не
// должна стоять запись. INSERT IGNORE не даёт упасть гонке двух ensure (PK гарантирует одну строку)
// и заодно глотает нарушение FK удалённого на лету прогона — повторное чтение ниже отдаёт это как
// sql.ErrNoRows.
func (s *Store) EnsureRunPackAccess(ctx context.Context, runID int) (*entity.ProductionRunPackAccess, error) {
	row, err := s.GetRunPackAccess(ctx, runID)
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("load production run pack access row: %w", err)
	}
	if err := storeutil.ExecNamed(ctx, s.DB,
		`INSERT IGNORE INTO production_run_pack_access (run_id) VALUES (:id)`,
		map[string]any{"id": runID}); err != nil {
		return nil, fmt.Errorf("ensure production run pack access row: %w", err)
	}
	return s.GetRunPackAccess(ctx, runID)
}

// RecordRunPackAccess сворачивает отложенную пачку счётчиков в строки. Best effort, как и
// RecordCardViewerAccess: аудит — это строка лога на каждый запрос, а счётчики нужны экрану.
func (s *Store) RecordRunPackAccess(ctx context.Context, counts map[int]int64, last map[int]time.Time) error {
	var firstErr error
	for id, n := range counts {
		if err := storeutil.ExecNamed(ctx, s.DB,
			`UPDATE production_run_pack_access SET access_count = access_count + :n, last_access_at = :last
			 WHERE run_id = :id`,
			map[string]any{"id": id, "n": n, "last": last[id]}); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("record production run pack access: %w", err)
		}
	}
	return firstErr
}
