package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

func requireLocalModelPoseDB(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container")
	}
}

func storeMannequinPoseFixture() entity.ModelMannequinPoseV2 {
	return entity.ModelMannequinPoseV2{
		ArmMode: entity.ModelMannequinArmModeLinked,
		LegMode: entity.ModelMannequinLegModeLinked,
	}
}

func addModelForPoseTest(ctx context.Context, t *testing.T, s *MYSQLStore, label string) int {
	t.Helper()
	id, err := s.Models().AddModel(ctx, &entity.ModelInsert{Name: fmt.Sprintf("%s-%d", label, time.Now().UnixNano())})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Models().DeleteModel(context.Background(), id) })
	return id
}

func TestModelMannequinPoseCASAndModelIsolation(t *testing.T) {
	requireLocalModelPoseDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := *testCfg
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	modelID := addModelForPoseTest(ctx, t, s, "pose-cas")

	absent, err := s.Models().GetModelMannequinPose(ctx, modelID)
	require.NoError(t, err)
	require.Nil(t, absent)
	guard, err := entity.NewModelMannequinPoseRevisionGuard(nil)
	require.NoError(t, err)
	created, err := s.Models().PutModelMannequinPose(ctx, modelID, storeMannequinPoseFixture(), guard, "first@admin")
	require.NoError(t, err)
	require.Equal(t, uint64(1), created.Revision)
	require.Equal(t, "first@admin", created.UpdatedBy)

	// A normal ModelInsert full replacement must not own or erase this resource.
	require.NoError(t, s.Models().UpdateModel(ctx, modelID, &entity.ModelInsert{Name: "renamed model"}))
	afterModelSave, err := s.Models().GetModelMannequinPose(ctx, modelID)
	require.NoError(t, err)
	require.Equal(t, created.CanonicalSHA256, afterModelSave.CanonicalSHA256)
	require.Equal(t, uint64(1), afterModelSave.Revision)

	stale := uint64(9)
	staleGuard, err := entity.NewModelMannequinPoseRevisionGuard(&stale)
	require.NoError(t, err)
	_, err = s.Models().PutModelMannequinPose(ctx, modelID, storeMannequinPoseFixture(), staleGuard, "stale@admin")
	var conflict *entity.ModelMannequinPoseConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, uint64(1), conflict.CurrentRevision)

	one := uint64(1)
	exactGuard, err := entity.NewModelMannequinPoseRevisionGuard(&one)
	require.NoError(t, err)
	nextPose := storeMannequinPoseFixture()
	nextPose.HeadYawDegrees = 10
	updated, err := s.Models().PutModelMannequinPose(ctx, modelID, nextPose, exactGuard, "second@admin")
	require.NoError(t, err)
	require.Equal(t, uint64(2), updated.Revision)
	require.Equal(t, int32(10), updated.Pose.HeadYawDegrees)
	require.Equal(t, "second@admin", updated.UpdatedBy)

	require.NoError(t, s.Models().DeleteModel(ctx, modelID))
	_, err = s.Models().GetModelMannequinPose(ctx, modelID)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestModelMannequinPoseConcurrentFirstWrite(t *testing.T) {
	requireLocalModelPoseDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := *testCfg
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	modelID := addModelForPoseTest(ctx, t, s, "pose-race")
	guard, err := entity.NewModelMannequinPoseRevisionGuard(nil)
	require.NoError(t, err)

	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = s.Models().PutModelMannequinPose(ctx, modelID, storeMannequinPoseFixture(), guard, fmt.Sprintf("writer-%d", i))
		}(i)
	}
	close(start)
	wg.Wait()

	successes, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, entity.ErrModelMannequinPoseConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	stored, err := s.Models().GetModelMannequinPose(ctx, modelID)
	require.NoError(t, err)
	require.Equal(t, uint64(1), stored.Revision)
}

func TestModelMannequinPoseCorruptionFailsClosed(t *testing.T) {
	requireLocalModelPoseDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := *testCfg
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	modelID := addModelForPoseTest(ctx, t, s, "pose-corrupt")
	guard, err := entity.NewModelMannequinPoseRevisionGuard(nil)
	require.NoError(t, err)
	_, err = s.Models().PutModelMannequinPose(ctx, modelID, storeMannequinPoseFixture(), guard, "writer")
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `UPDATE model_mannequin_pose SET canonical_sha256 = REPEAT('0', 64) WHERE model_id = ?`, modelID)
	require.NoError(t, err)

	_, err = s.Models().GetModelMannequinPose(ctx, modelID)
	require.ErrorIs(t, err, entity.ErrModelMannequinPoseCorrupt)
	one := uint64(1)
	exactGuard, err := entity.NewModelMannequinPoseRevisionGuard(&one)
	require.NoError(t, err)
	_, err = s.Models().PutModelMannequinPose(ctx, modelID, storeMannequinPoseFixture(), exactGuard, "writer")
	require.ErrorIs(t, err, entity.ErrModelMannequinPoseCorrupt)
}
