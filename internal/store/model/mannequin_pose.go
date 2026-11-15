package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

type modelMannequinPoseRow struct {
	ModelID         int       `db:"model_id"`
	SchemaVersion   uint32    `db:"schema_version"`
	PoseSpace       string    `db:"pose_space"`
	Payload         []byte    `db:"payload"`
	CanonicalSHA256 string    `db:"canonical_sha256"`
	Revision        uint64    `db:"revision"`
	UpdatedAt       time.Time `db:"updated_at"`
	UpdatedBy       string    `db:"updated_by"`
}

const modelMannequinPoseSelect = `
	SELECT model_id, schema_version, pose_space, payload, canonical_sha256,
		revision, updated_at, updated_by
	FROM model_mannequin_pose
	WHERE model_id = :model_id`

func (row modelMannequinPoseRow) stored() (*entity.StoredModelMannequinPose, error) {
	if row.SchemaVersion != entity.ModelMannequinPoseSchemaVersionV2 ||
		row.PoseSpace != entity.ModelMannequinPoseSpaceDirectV9 {
		return nil, entity.ErrModelMannequinPoseCorrupt
	}
	pose, err := entity.DecodeModelMannequinPoseCanonicalJSON(row.Payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", entity.ErrModelMannequinPoseCorrupt, err)
	}
	stored := &entity.StoredModelMannequinPose{
		ModelID:         row.ModelID,
		Pose:            pose,
		CanonicalSHA256: row.CanonicalSHA256,
		Revision:        row.Revision,
		UpdatedAt:       row.UpdatedAt,
		UpdatedBy:       row.UpdatedBy,
	}
	if err := stored.ValidateIntegrity(); err != nil {
		return nil, err
	}
	return stored, nil
}

func loadModelMannequinPose(ctx context.Context, rep dependency.Repository, modelID int, forUpdate bool) (*entity.StoredModelMannequinPose, error) {
	query := modelMannequinPoseSelect
	if forUpdate {
		query += " FOR UPDATE"
	}
	row, err := storeutil.QueryNamedOne[modelMannequinPoseRow](ctx, rep.DB(), query, map[string]any{"model_id": modelID})
	if err != nil {
		return nil, err
	}
	return row.stored()
}

// GetModelMannequinPose returns nil, nil when the model exists but has no pose.
// The two reads share one repeatable-read snapshot so a concurrent cascading
// model delete cannot turn a missing model into an apparently absent pose.
func (s *Store) GetModelMannequinPose(ctx context.Context, modelID int) (*entity.StoredModelMannequinPose, error) {
	var stored *entity.StoredModelMannequinPose
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if _, err := storeutil.QueryNamedOne[struct {
			ID int `db:"id"`
		}](ctx, rep.DB(), `SELECT id FROM model WHERE id = :id`, map[string]any{"id": modelID}); err != nil {
			return err
		}
		var err error
		stored, err = loadModelMannequinPose(ctx, rep, modelID, false)
		if errors.Is(err, sql.ErrNoRows) {
			stored = nil
			return nil
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("get model mannequin pose: %w", err)
	}
	return stored, nil
}

// PutModelMannequinPose creates or replaces the pose under a presence-aware
// uint64 CAS. The parent model row is locked first, serialising concurrent first
// writes and model deletion before the child row exists.
func (s *Store) PutModelMannequinPose(
	ctx context.Context,
	modelID int,
	pose entity.ModelMannequinPoseV2,
	expected entity.ModelMannequinPoseRevisionGuard,
	updatedBy string,
) (*entity.StoredModelMannequinPose, error) {
	updatedBy = strings.TrimSpace(updatedBy)
	if modelID <= 0 {
		return nil, fmt.Errorf("model id must be positive")
	}
	if updatedBy == "" || len(updatedBy) > 255 {
		return nil, fmt.Errorf("updated_by must contain 1..255 characters")
	}
	payload, err := pose.CanonicalDocumentJSON()
	if err != nil {
		return nil, err
	}
	canonicalSHA256, err := pose.CanonicalSHA256()
	if err != nil {
		return nil, err
	}

	var stored *entity.StoredModelMannequinPose
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if _, err := storeutil.QueryNamedOne[struct {
			ID int `db:"id"`
		}](ctx, rep.DB(), `SELECT id FROM model WHERE id = :id FOR UPDATE`, map[string]any{"id": modelID}); err != nil {
			return err
		}

		current, err := loadModelMannequinPose(ctx, rep, modelID, true)
		if errors.Is(err, sql.ErrNoRows) {
			current = nil
		} else if err != nil {
			return err
		}
		currentRevision := uint64(0)
		if current != nil {
			currentRevision = current.Revision
		}
		if expected.Conflicts(currentRevision) {
			return &entity.ModelMannequinPoseConflictError{CurrentRevision: currentRevision}
		}

		params := map[string]any{
			"model_id":         modelID,
			"schema_version":   entity.ModelMannequinPoseSchemaVersionV2,
			"pose_space":       entity.ModelMannequinPoseSpaceDirectV9,
			"payload":          string(payload),
			"canonical_sha256": canonicalSHA256,
			"updated_by":       updatedBy,
		}
		if current == nil {
			if err := storeutil.ExecNamed(ctx, rep.DB(), `
				INSERT INTO model_mannequin_pose
					(model_id, schema_version, pose_space, payload, canonical_sha256, revision, updated_by)
				VALUES
					(:model_id, :schema_version, :pose_space, :payload, :canonical_sha256, 1, :updated_by)`, params); err != nil {
				return fmt.Errorf("insert model mannequin pose: %w", err)
			}
		} else {
			if currentRevision == math.MaxUint64 {
				return entity.ErrModelMannequinPoseRevisionExhausted
			}
			params["expected_revision"] = currentRevision
			rows, err := storeutil.ExecNamedRows(ctx, rep.DB(), `
				UPDATE model_mannequin_pose SET
					schema_version = :schema_version,
					pose_space = :pose_space,
					payload = :payload,
					canonical_sha256 = :canonical_sha256,
					revision = revision + 1,
					updated_at = CURRENT_TIMESTAMP(6),
					updated_by = :updated_by
				WHERE model_id = :model_id AND revision = :expected_revision`, params)
			if err != nil {
				return fmt.Errorf("update model mannequin pose: %w", err)
			}
			if rows != 1 {
				return &entity.ModelMannequinPoseConflictError{CurrentRevision: currentRevision}
			}
		}
		stored, err = loadModelMannequinPose(ctx, rep, modelID, false)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("put model mannequin pose: %w", err)
	}
	return stored, nil
}
