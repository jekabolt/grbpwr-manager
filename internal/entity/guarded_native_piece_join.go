package entity

import (
	"context"
	"io"
)

type JoinPatternAsset struct {
	URL    string
	Size   int64
	SHA256 string
}
type JoinDirectedEdge struct{ Curve, Start, End string }
type JoinBoundarySelection struct {
	FixedInterval, MovingInterval        JoinDirectedEdge
	CardID                               int
	FabricPurpose, BOMLineKey, SizeToken string
	SizeID                               int
	Fixed, Moving, Result                string
	FixedEdge, MovingEdge                int
	TranslateX, TranslateY               int64 // millionths of a millimetre, the ordinary writer precision
}

type JoinPatternDocument struct {
	Row                                        SavedPatternRowIdentity
	AssemblyRevision, CompleteSourceSHA256     string
	FixedLineKey, MovingLineKey, ResultLineKey string
}

// Internal to the ordinary document write. The store accepts only the configured
// managed bucket and performs authentication itself under the card lock.
type GuardedNativePieceJoin struct {
	Document        JoinPatternDocument
	Selection       JoinBoundarySelection
	Reviewed        JoinPatternAsset
	Filename        string
	ManagedPatterns interface {
		GetManagedObject(context.Context, string) (io.ReadCloser, int64, error)
	}
}
