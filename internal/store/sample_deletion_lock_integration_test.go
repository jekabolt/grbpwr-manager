package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestSampleDeletionFactsLockOutConcurrentIssue доказывает НЕСУЩЕЕ утверждение удаления семпла:
// пере-проверка фактов ВНУТРИ транзакции закрывает гонку, а не просто повторяет вопрос.
//
// Дыра, которой боишься: сухой прогон сказал «материал возвращён», оператор нажал «удалить», и
// РОВНО между чтением и DELETE соседняя вкладка списала на этот семпл ткань. Если бы чтение фактов
// было обычным снимком (REPEATABLE READ), транзакция удаления новой строки НЕ УВИДЕЛА БЫ, снесла
// семпл, а списание осталось бы в ленте с sample_id = NULL — то есть ткань числилась бы
// израсходованной ни на что. Ровно этого удаление и обязано не допустить.
//
// Спасает не код удаления, а УРОВЕНЬ ИЗОЛЯЦИИ: пишущие транзакции стора идут в SERIALIZABLE
// (MYSQLStore.Tx), где InnoDB превращает обычный SELECT в блокирующее чтение с next-key локами по
// просканированному диапазону. Утверждение проверяемое — и проверяется здесь на настоящем MySQL, а
// не берётся из документации: держим тот же запрос в такой же транзакции и пытаемся вставить
// движение по этому семплу с другого соединения. Вставка обязана уткнуться в таймаут ожидания
// блокировки (1205), а не пройти.
func TestSampleDeletionFactsLockOutConcurrentIssue(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	matID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "SMP-LOCK Fabric", Section: "fabric", Unit: sql.NullString{String: "m", Valid: true},
	})
	require.NoError(t, err)
	techCardID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber:     sql.NullString{String: "SMP-LOCK-1", Valid: true},
		Name:            "sample deletion lock",
		Stage:           entity.TechCardStageProto,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
	})
	require.NoError(t, err)
	sampleID, err := s.Samples().AddSample(ctx, &entity.SampleInsert{
		TechCardId: techCardID, Purpose: entity.SamplePurposeProto,
		Status: entity.SampleStatusPlanned, FabricSource: entity.SampleFabricSample,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE sample_id = ?", sampleID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", techCardID) // каскадом уносит семпл
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", matID)
	})

	// Транзакция-«удаление»: тот же уровень изоляции, что у MYSQLStore.Tx, и то же чтение фактов,
	// что делает readSampleDeletionFacts. Держим её открытой — это и есть окно гонки.
	holder, err := testDB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	require.NoError(t, err)
	defer func() { _ = holder.Rollback() }()
	var qty sql.NullString
	err = holder.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE mv.movement_type
			WHEN 'issue_sample'  THEN mv.quantity
			WHEN 'return_sample' THEN -mv.quantity ELSE 0 END), 0) AS qty
		FROM material_stock_movement mv WHERE mv.sample_id = ?`, sampleID).Scan(&qty)
	require.NoError(t, err)

	// Соседняя вкладка списывает ткань на тот же семпл. Своё соединение и короткий таймаут: нам
	// нужен ОТВЕТ («жду блокировку»), а не полминуты ожидания в тесте.
	conn, err := testDB.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = conn.ExecContext(ctx, "SET SESSION innodb_lock_wait_timeout = 2")
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `
		INSERT INTO material_stock_movement
			(material_id, movement_type, quantity, on_hand_before, on_hand_after, sample_id, tech_card_id)
		VALUES (?, 'issue_sample', 1.000, 10.000, 9.000, ?, ?)`, matID, sampleID, techCardID)
	require.Error(t, err, "конкурентное списание обязано ждать транзакцию удаления, а не проскочить в её окно")
	var me *mysql.MySQLError
	require.ErrorAs(t, err, &me)
	require.Equal(t, uint16(1205), me.Number, "ждём таймаут ожидания блокировки, получили: %v", err)

	// Как только окно закрылось, то же списание проходит: удаление ничего не запрещает НАВСЕГДА,
	// оно лишь не даёт списанию встать между проверкой и DELETE.
	require.NoError(t, holder.Rollback())
	_, err = conn.ExecContext(ctx, `
		INSERT INTO material_stock_movement
			(material_id, movement_type, quantity, on_hand_before, on_hand_after, sample_id, tech_card_id)
		VALUES (?, 'issue_sample', 1.000, 10.000, 9.000, ?, ?)`, matID, sampleID, techCardID)
	require.NoError(t, err)

	// И теперь вердикт видит невозвращённый метр — то есть удаление в этот момент откажет.
	v, _, err := s.Samples().EvaluateSampleDeletion(ctx, sampleID)
	require.NoError(t, err)
	require.False(t, v.Deletable)
	require.Equal(t, entity.SampleBlockerMaterialOutstanding, v.Blockers[0].Reason)
}
