package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// SavedPatternRowIdentityToPb projects the saved row, before adding an ephemeral download URL.
func SavedPatternRowIdentityToPb(row entity.SavedPatternRowIdentity) *pb_admin.TechCardSavedPatternRowIdentity {
	return &pb_admin.TechCardSavedPatternRowIdentity{
		TechCardId:       int32(row.TechCardID),
		ScopeKey:         row.ScopeKey,
		PatternLineKey:   row.PatternLineKey,
		SavedRowRevision: row.SavedRowRevision,
		CardLockVersion:  int32(row.CardLockVersion),
		Url:              row.URL,
		SizeBytes:        row.SizeBytes,
		Sha256:           row.SHA256,
		PatternVersion:   int32(row.PatternVersion),
	}
}

// SavedAssemblyCompleteSourceV1ToPb is shared by the RPC and its source seal.
func SavedAssemblyCompleteSourceV1ToPb(source *entity.SavedAssemblyCompleteSourceV1) *pb_admin.TechCardSavedAssemblyCompleteSourceV1 {
	if source == nil {
		return nil
	}
	out := &pb_admin.TechCardSavedAssemblyCompleteSourceV1{
		Rows:          make([]*pb_admin.TechCardSavedPatternRowIdentity, 0, len(source.Rows)),
		OwnedSeams:    TechCardSeamsToPb(source.OwnedSeams),
		Aliases:       make([]*pb_admin.TechCardSavedAssemblySourceAliasV1, 0, len(source.Aliases)),
		GarmentLabels: techCardGarmentLabelsToPb(source.GarmentLabels),
		SourceSha256:  source.SourceSHA256,
		CanonicalJson: append([]byte(nil), source.CanonicalJSON...),
	}
	for _, row := range source.Rows {
		out.Rows = append(out.Rows, SavedPatternRowIdentityToPb(row))
	}
	for _, alias := range source.Aliases {
		out.Aliases = append(out.Aliases, &pb_admin.TechCardSavedAssemblySourceAliasV1{
			ScopeKey: alias.ScopeKey, BlockName: alias.BlockName, PieceLineKey: alias.PieceLineKey,
		})
	}
	return out
}

// SavedAssemblyAuthoredFoldSourceV1ToPb is shared by the RPC and the authored-fold seal.
func SavedAssemblyAuthoredFoldSourceV1ToPb(source *entity.SavedAssemblyAuthoredFoldSourceV1) *pb_admin.TechCardSavedAssemblyAuthoredFoldSourceV1 {
	out := &pb_admin.TechCardSavedAssemblyAuthoredFoldSourceV1{
		CompleteSourceSha256: source.CompleteSourceSHA256,
		SelectedSizeId:       int32(source.SelectedSizeID),
		SelectedSizeToken:    source.SelectedSizeToken,
		Folds:                make([]*pb_admin.TechCardSavedAssemblyAuthoredFoldV1, 0, len(source.Folds)),
		SourceSha256:         source.SourceSHA256,
		CanonicalJson:        append([]byte(nil), source.CanonicalJSON...),
	}
	folds := make([]entity.TechCardPieceFold, 0, len(source.Folds))
	for _, entry := range source.Folds {
		folds = append(folds, entry.Fold)
	}
	for i, fold := range TechCardPieceFoldsToPb(folds) {
		entry := source.Folds[i]
		out.Folds = append(out.Folds, &pb_admin.TechCardSavedAssemblyAuthoredFoldV1{
			OperationNumber:   entry.OperationNumber,
			OperationOrder:    int32(entry.OperationOrder),
			Fold:              fold,
			SourceFingerprint: entry.Fold.SourceFingerprint,
		})
	}
	return out
}
