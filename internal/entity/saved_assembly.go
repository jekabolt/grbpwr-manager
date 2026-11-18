package entity

import "errors"

var (
	ErrTechCardSavedAssemblyMoved       = errors.New("saved assembly authority moved")
	ErrTechCardSavedAssemblyUnsupported = errors.New("saved assembly topology is unsupported")
)

// SavedPatternRowIdentity is the exact eight-field byte-row authority used by the DXF reprocessor.
// SavedRowRevision is deliberately not the human-facing pattern Version.
type SavedPatternRowIdentity struct {
	TechCardID       int
	ScopeKey         string
	PatternLineKey   string
	SavedRowRevision uint32
	CardLockVersion  int
	URL              string
	SizeBytes        int64
	SHA256           string
	PatternVersion   int
	Filename         string
}

type SavedAssemblySnapshotRequest struct {
	ExpectedRow                      SavedPatternRowIdentity
	SelectedOperationNumber          int
	ExpectedServerAssemblyRevision   string
	ExpectedTopologySHA256           string
	Profile                          SavedAssemblySnapshotProfile
	ExpectedCompleteSourceSHA256     string
	SelectedSizeID                   int
	ExpectedAuthoredFoldSourceSHA256 string
}

type SavedAssemblySnapshotProfile int32

const (
	SavedAssemblySnapshotSelected SavedAssemblySnapshotProfile = iota
	SavedAssemblySnapshotCompleteSourceV1
	SavedAssemblySnapshotCompleteSourceWithAuthoredFoldsV1
)

type SavedAssemblySourceAliasV1 struct {
	ScopeKey     string
	BlockName    string
	PieceLineKey string
}

// SavedAssemblyCompleteSourceV1 commits persisted inputs only. It cannot authorize a DXF edge,
// convert an old POSE to physical placement, or imply that any seam is ready for simulation.
type SavedAssemblyCompleteSourceV1 struct {
	Rows          []SavedPatternRowIdentity
	OwnedSeams    []TechCardSeam
	Aliases       []SavedAssemblySourceAliasV1
	GarmentLabels []TechCardGarmentLabel
	SourceSHA256  string
	CanonicalJSON []byte
}

type SavedAssemblyAuthoredFoldV1 struct {
	OperationNumber int32
	OperationOrder  int
	Fold            TechCardPieceFold
}

// SavedAssemblyAuthoredFoldSourceV1 attests persisted AO-owned folds and the current selected
// size identity. It supplies source witnesses for native geometry resolution at that rank.
type SavedAssemblyAuthoredFoldSourceV1 struct {
	CompleteSourceSHA256 string
	SelectedSizeID       int
	SelectedSizeToken    string
	Folds                []SavedAssemblyAuthoredFoldV1
	SourceSHA256         string
	CanonicalJSON        []byte
}

type SavedAssemblySnapshot struct {
	Row                     SavedPatternRowIdentity
	SelectedOperationNumber int
	Card                    *TechCard
	SelectedSeams           []TechCardSeam
	ServerAssemblyRevision  string
	TopologySHA256          string
	TopologyCanonicalJSON   []byte
	SemanticSidecar         *PatternSemanticSidecar
	CompleteSourceV1        *SavedAssemblyCompleteSourceV1
	AuthoredFoldSourceV1    *SavedAssemblyAuthoredFoldSourceV1
}
