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
	ExpectedRow                    SavedPatternRowIdentity
	SelectedOperationNumber        int
	ExpectedServerAssemblyRevision string
	ExpectedTopologySHA256         string
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
}
