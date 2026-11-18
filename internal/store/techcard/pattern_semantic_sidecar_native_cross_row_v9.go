package techcard

import (
	"context"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/model"
)

func isNativeCrossRowCarrier(media string) bool {
	return media == entity.PatternProductCompositeSidecarV8MediaType ||
		media == entity.PatternProductCompositeSidecarV9MediaType ||
		media == entity.PatternProductCompositeSidecarV10MediaType
}

func validateNativePlacementWrite(
	ctx context.Context, db dependency.DB, in entity.PatternSemanticSidecarWrite, prior *priorPatternSemanticSidecar,
	current entity.PatternProductNativeCrossRowCurrentV8,
) error {
	if prior == nil || !isNativeCrossRowCarrier(prior.MediaType) {
		return fmt.Errorf("native placement requires a current native carrier: %w", entity.ErrTechCardPatternSidecarBindingMoved)
	}
	stored := entity.PatternSemanticSidecar{
		TechCardId: in.TechCardId, PatternLineKey: in.PatternLineKey,
		CardLockVersion: prior.CardLockVersion, SavedRowRevision: prior.SavedRowRevision,
		DxfSHA256: prior.DxfSHA256, PatternVersion: prior.PatternVersion, SidecarSHA256: prior.SHA256,
		MediaType: prior.MediaType, ByteLength: prior.ByteLength, Payload: prior.Payload,
		PostSaveReceiptSHA256: prior.PostSaveReceiptSHA256,
	}
	var binding entity.NeutralPlacementModelAuthorityV1
	var err error
	if in.MediaType == entity.PatternProductCompositeSidecarV10MediaType {
		binding, err = entity.CheckPatternProductNativeWholePlacementTransitionV10(in, stored)
	} else {
		binding, err = entity.CheckPatternProductNativeCrossRowPlacementTransitionV9(in, stored)
	}
	if err != nil {
		return err
	}
	// Keep this lock after card → pattern → prior and inside the same writable
	// transaction. The ordinary model and pose writers serialize on it too.
	lockedModel, err := model.LockNeutralPlacementModelAuthorityV1(ctx, db, binding.ModelID)
	if err != nil {
		return nativeCrossRowPlacementModelError(err)
	}
	if in.MediaType == entity.PatternProductCompositeSidecarV10MediaType {
		return entity.ValidatePatternProductNativeWholePlacementWriteCurrentV10(in, current, lockedModel)
	}
	return entity.ValidatePatternProductNativeCrossRowPlacementWriteCurrentV9(in,
		entity.PatternProductNativeCrossRowPlacementCurrentV9{Source: current, Model: lockedModel})
}

func validateStoredNativePlacement(
	ctx context.Context, db dependency.DB, stored entity.PatternSemanticSidecar, current entity.PatternProductNativeCrossRowCurrentV8,
) error {
	var binding entity.NeutralPlacementModelAuthorityV1
	var err error
	if stored.MediaType == entity.PatternProductCompositeSidecarV10MediaType {
		binding, err = entity.ReadPatternProductNativeWholePlacementBindingV10(stored)
	} else {
		binding, err = entity.ReadPatternProductNativeCrossRowPlacementBindingV9(stored)
	}
	if err != nil {
		return err
	}
	snapshotModel, err := model.ReadNeutralPlacementModelAuthorityV1(ctx, db, binding.ModelID)
	if err != nil {
		return nativeCrossRowPlacementModelError(err)
	}
	if stored.MediaType == entity.PatternProductCompositeSidecarV10MediaType {
		return entity.ValidateStoredPatternProductNativeWholePlacementV10(stored, current, snapshotModel)
	}
	return entity.ValidateStoredPatternProductNativeCrossRowPlacementV9(stored,
		entity.PatternProductNativeCrossRowPlacementCurrentV9{Source: current, Model: snapshotModel})
}

func nativeCrossRowPlacementModelError(err error) error {
	switch {
	case errors.Is(err, entity.ErrNeutralPlacementModelMoved):
		return fmt.Errorf("%w: %w", entity.ErrTechCardPatternSidecarBindingMoved, err)
	case errors.Is(err, model.ErrNeutralPlacementModelCorrupt), errors.Is(err, entity.ErrModelMannequinPoseCorrupt):
		return fmt.Errorf("%w: %w", entity.ErrTechCardPatternSidecarCorrupt, err)
	default:
		return err
	}
}
