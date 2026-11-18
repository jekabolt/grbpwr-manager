package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const NeutralPlacementPoseKindV1 = entity.NeutralPlacementPoseKindV1

var (
	ErrNeutralPlacementModelMoved   = entity.ErrNeutralPlacementModelMoved
	ErrNeutralPlacementModelCorrupt = errors.New("neutral placement model inputs are invalid")
)

type NeutralPlacementPoseBindingV1 = entity.NeutralPlacementPoseBindingV1
type NeutralPlacementModelAuthorityV1 = entity.NeutralPlacementModelAuthorityV1

// ReadNeutralPlacementModelAuthorityV1 borrows the caller's repeatable-read
// snapshot. The caller must encompass all other source reads in that snapshot.
func ReadNeutralPlacementModelAuthorityV1(ctx context.Context, db dependency.DB, modelID int) (NeutralPlacementModelAuthorityV1, error) {
	return loadNeutralPlacementModelAuthorityV1(ctx, db, modelID, false)
}

// LockNeutralPlacementModelAuthorityV1 borrows the caller's writable transaction.
// It pins the model through the caller's commit, including when no pose exists.
func LockNeutralPlacementModelAuthorityV1(ctx context.Context, db dependency.DB, modelID int) (NeutralPlacementModelAuthorityV1, error) {
	return loadNeutralPlacementModelAuthorityV1(ctx, db, modelID, true)
}

func loadNeutralPlacementModelAuthorityV1(ctx context.Context, db dependency.DB, modelID int, lock bool) (NeutralPlacementModelAuthorityV1, error) {
	var out NeutralPlacementModelAuthorityV1
	// A pool would release the model lock at the end of its SELECT and allow
	// the next query to observe a different transaction.
	if _, transactional := db.(interface {
		Commit() error
		Rollback() error
	}); !transactional {
		return out, fmt.Errorf("neutral placement model authority requires an enclosing transaction")
	}
	query := `SELECT gender, updated_at FROM model WHERE id = :id`
	if lock {
		query += " FOR UPDATE"
	}
	row, err := storeutil.QueryNamedOne[struct {
		Gender    entity.GenderEnum `db:"gender"`
		UpdatedAt time.Time         `db:"updated_at"`
	}](ctx, db, query, map[string]any{"id": modelID})
	if errors.Is(err, sql.ErrNoRows) {
		return out, fmt.Errorf("model %d is missing: %w", modelID, ErrNeutralPlacementModelMoved)
	}
	if err != nil {
		return out, err
	}

	_, err = loadModelMannequinPose(ctx, db, modelID, lock)
	if err == nil {
		return out, fmt.Errorf("model %d has a stored pose: %w", modelID, ErrNeutralPlacementModelMoved)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}

	measurements, err := measurementsByModelIds(ctx, db, []int{modelID})
	if err != nil {
		return out, err
	}
	digest, err := entity.PersistedModelPhysicalInputsSHA256V1(modelID, row.Gender, measurements[modelID])
	if err != nil {
		return out, fmt.Errorf("%w: %v", ErrNeutralPlacementModelCorrupt, err)
	}
	timestamp := timestamppb.New(row.UpdatedAt)
	if row.UpdatedAt.IsZero() || !timestamp.IsValid() {
		return out, fmt.Errorf("invalid model updated_at: %w", ErrNeutralPlacementModelCorrupt)
	}
	timestampJSON, err := protojson.Marshal(timestamp)
	if err != nil {
		return out, err
	}

	neutralSHA, err := entity.NeutralPlacementPoseCanonicalSHA256V1()
	if err != nil {
		return out, err
	}
	return NeutralPlacementModelAuthorityV1{
		ModelID:                   modelID,
		ModelUpdatedAt:            strings.Trim(string(timestampJSON), `"`),
		ModelPhysicalInputsSHA256: digest,
		Pose: NeutralPlacementPoseBindingV1{
			Kind:            NeutralPlacementPoseKindV1,
			CanonicalSHA256: neutralSHA,
		},
	}, nil
}
