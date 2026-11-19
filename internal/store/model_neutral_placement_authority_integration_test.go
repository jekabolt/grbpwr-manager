package store

import (
	"context"
	"database/sql"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNeutralPlacementModelAuthorityBorrowedInnoDB(t *testing.T) {
	requireLocalModelPoseDB(t)
	if os.Getenv("MYSQL_DATABASE") != "assembly_v9_model_owner_test" {
		t.Skip("requires a disposable assembly_v9_model_owner_test schema with performance_schema enabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	repository, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)
	t.Cleanup(repository.Close)
	models := repository.Models()
	neutralPose := storeMannequinPoseFixture()
	absent, err := entity.NewModelMannequinPoseRevisionGuard(nil)
	require.NoError(t, err)

	newModel := func(t *testing.T) (int, entity.ModelInsert) {
		t.Helper()
		spec := entity.ModelInsert{
			Name: t.Name(), Gender: sql.NullString{String: string(entity.Unisex), Valid: true},
			BodyMeasurementSchemaVersion: entity.BodyMeasurementSchemaVersionCurrent,
			Measurements: []entity.ModelMeasurement{
				{Name: entity.BodyHeight, ValueMM: 1720},
				{Name: entity.BodyWaist, ValueMM: 740},
			},
		}
		id, err := models.AddModel(ctx, &spec)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, models.DeleteModel(context.Background(), id)) })
		return id, spec
	}
	read := func(id int) (snapshot model.NeutralPlacementModelAuthorityV1, err error) {
		err = repository.readTx(ctx, func(ctx context.Context, tx dependency.Repository) error {
			var readErr error
			snapshot, readErr = model.ReadNeutralPlacementModelAuthorityV1(ctx, tx.DB(), id)
			return readErr
		})
		return snapshot, err
	}

	t.Run("measurement writer waits and same timestamp moves content", func(t *testing.T) {
		id, spec := newModel(t)
		current, err := read(id)
		require.NoError(t, err)
		require.Equal(t, "neutral-default-direct-v9", current.Pose.Kind)
		require.Equal(t, "2923b9188bbd3fd1b4bc25e68ef783a88751e34cad1a9c7b6e784b3e689ca86d", current.Pose.CanonicalSHA256)
		require.NoError(t, current.VerifyExpected(current))

		tx, err := repository.TxBegin(ctx)
		require.NoError(t, err)
		defer tx.TxRollback(ctx)
		pinned, err := model.LockNeutralPlacementModelAuthorityV1(ctx, tx.DB(), id)
		require.NoError(t, err)
		require.NoError(t, pinned.VerifyExpected(current))
		spec.Measurements[1].ValueMM = 745
		write := make(chan error, 1)
		go func() { write <- models.UpdateModel(ctx, id, &spec) }()
		requireModelParentWait(t, ctx, id)
		require.NoError(t, tx.TxCommit(ctx))
		require.NoError(t, <-write)

		fresh, err := read(id)
		require.NoError(t, err)
		require.Equal(t, current.ModelUpdatedAt, fresh.ModelUpdatedAt, "ordinary writer leaves unchanged header timestamp")
		require.NotEqual(t, current.ModelPhysicalInputsSHA256, fresh.ModelPhysicalInputsSHA256)
		require.ErrorIs(t, fresh.VerifyExpected(current), model.ErrNeutralPlacementModelMoved)
	})

	t.Run("first pose writer waits even when pose row is absent", func(t *testing.T) {
		id, _ := newModel(t)
		tx, err := repository.TxBegin(ctx)
		require.NoError(t, err)
		defer tx.TxRollback(ctx)
		_, err = model.LockNeutralPlacementModelAuthorityV1(ctx, tx.DB(), id)
		require.NoError(t, err)
		write := make(chan error, 1)
		go func() {
			_, err := models.PutModelMannequinPose(ctx, id, neutralPose, absent, "local-fixture")
			write <- err
		}()
		requireModelParentWait(t, ctx, id)
		require.NoError(t, tx.TxCommit(ctx))
		require.NoError(t, <-write)
		_, err = read(id)
		require.ErrorIs(t, err, model.ErrNeutralPlacementModelMoved, "persisted neutral is distinct from proven absence")
	})

	t.Run("existing repeatable snapshot and fresh corruption refusal", func(t *testing.T) {
		id, _ := newModel(t)
		require.NoError(t, repository.readTx(ctx, func(ctx context.Context, tx dependency.Repository) error {
			current, err := model.ReadNeutralPlacementModelAuthorityV1(ctx, tx.DB(), id)
			require.NoError(t, err)
			_, err = models.PutModelMannequinPose(ctx, id, neutralPose, absent, "local-fixture")
			require.NoError(t, err)
			again, err := model.ReadNeutralPlacementModelAuthorityV1(ctx, tx.DB(), id)
			require.NoError(t, err)
			require.NoError(t, again.VerifyExpected(current))
			return nil
		}))
		_, err := read(id)
		require.ErrorIs(t, err, model.ErrNeutralPlacementModelMoved)
		_, err = testDB.ExecContext(ctx, "UPDATE model_mannequin_pose SET canonical_sha256 = ? WHERE model_id = ?", strings.Repeat("0", 64), id)
		require.NoError(t, err)
		_, err = read(id)
		require.ErrorIs(t, err, entity.ErrModelMannequinPoseCorrupt)
	})

	t.Run("invalid stored measurement refuses authority", func(t *testing.T) {
		id, _ := newModel(t)
		_, err := testDB.ExecContext(ctx, `UPDATE model_measurement SET measurement_value_mm = 5001
			WHERE model_id = ? AND measurement_name = 'waist'`, id)
		require.NoError(t, err)
		current, err := read(id)
		require.ErrorIs(t, err, model.ErrNeutralPlacementModelCorrupt)
		require.Empty(t, current)
	})

	t.Run("missing parent and accidental pool refuse", func(t *testing.T) {
		_, err := read(math.MaxInt32)
		require.ErrorIs(t, err, model.ErrNeutralPlacementModelMoved)
		_, err = model.ReadNeutralPlacementModelAuthorityV1(ctx, repository.DB(), 1)
		require.ErrorContains(t, err, "enclosing transaction")
	})
}

func requireModelParentWait(t *testing.T, ctx context.Context, modelID int) {
	t.Helper()
	// Observe an actual row-lock wait rather than estimating how much time a
	// racing goroutine had to run. The selected DB is the disposable test schema.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var waiting int
		err := testDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM performance_schema.data_lock_waits w
			JOIN performance_schema.data_locks l ON BINARY l.ENGINE_LOCK_ID = BINARY w.BLOCKING_ENGINE_LOCK_ID
			WHERE BINARY l.OBJECT_SCHEMA = BINARY DATABASE() AND l.OBJECT_NAME = 'model' AND l.INDEX_NAME = 'PRIMARY'
			  AND BINARY l.LOCK_DATA = BINARY CAST(? AS CHAR)`, modelID).Scan(&waiting)
		require.NoError(c, err)
		require.Positive(c, waiting)
	}, 5*time.Second, 20*time.Millisecond)
}
