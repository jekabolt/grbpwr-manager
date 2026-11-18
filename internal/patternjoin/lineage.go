package patternjoin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"regexp"
	"strings"
)

// Document names the identities the store must independently re-read under the ordinary
// UpdateTechCard lock. A valid native signature does not establish that they still exist.
type Document struct {
	Row                                        entity.SavedPatternRowIdentity
	AssemblyRevision, CompleteSourceSHA256     string
	FixedLineKey, MovingLineKey, ResultLineKey string
}

// CheckSavedDocument is injected by the server, within its existing card-row transaction.
// Its concrete store owner reads the complete Assembly snapshot and both exact alias groups.
type CheckSavedDocument func(context.Context, Document, Selection) error

type Lineage struct {
	Kind     string
	Document Document
	Native   Binding
}
type SignedLineage struct{ Payload, Signature []byte }

var assemblyClock = regexp.MustCompile(`^asm:v1:[a-f0-9]{32}:[0-9]{1,20}$`)

func marker(ctx context.Context, store ObjectStore, config *bucket.Config, doc Document, reviewed Asset, selected Selection) ([]byte, error) {
	r := doc.Row
	if r.TechCardID != selected.CardID || !entity.ValidTechCardSeamKey(r.PatternLineKey) || r.SavedRowRevision == 0 || r.CardLockVersion <= 0 || strings.TrimSpace(r.ScopeKey) == "" ||
		!assemblyClock.MatchString(doc.AssemblyRevision) || !canonicalSHA.MatchString(doc.CompleteSourceSHA256) {
		return nil, invalid
	}
	keys := map[string]bool{r.PatternLineKey: true}
	for _, k := range []string{doc.FixedLineKey, doc.MovingLineKey, doc.ResultLineKey} {
		if !entity.ValidTechCardSeamKey(k) || keys[k] {
			return nil, invalid
		}
		keys[k] = true
	}
	original := Asset{URL: r.URL, Size: r.SizeBytes, SHA256: r.SHA256}
	native, err := ReadBinding(ctx, store, config, original, reviewed, selected)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(Lineage{"grbpwr.saved-native-piece-join.v1", doc, native})
	if err != nil {
		return nil, err
	}
	if len(payload) > 16384 {
		return nil, invalid
	}
	return payload, nil
}

// SignNativeLineage serializes the two-source evidence after reading both managed files.
// Its key is supplied by the server owner. Viewer-access keys are unrelated to this format.
func SignNativeLineage(ctx context.Context, store ObjectStore, config *bucket.Config, doc Document, reviewed Asset, selected Selection, check CheckSavedDocument, key ed25519.PrivateKey) (SignedLineage, error) {
	if len(key) != ed25519.PrivateKeySize || check == nil {
		return SignedLineage{}, invalid
	}
	p, err := marker(ctx, store, config, doc, reviewed, selected)
	if err != nil {
		return SignedLineage{}, err
	}
	if err := check(ctx, doc, selected); err != nil {
		return SignedLineage{}, err
	}
	return SignedLineage{p, ed25519.Sign(key, p)}, nil
}

// MatchNativeLineage authenticates canonical server serialization against fresh expected
// inputs and bytes, rather than taking a row, native path or key from the signed payload.
func MatchNativeLineage(ctx context.Context, store ObjectStore, config *bucket.Config, doc Document, reviewed Asset, selected Selection, check CheckSavedDocument, key ed25519.PublicKey, signed SignedLineage) error {
	if check == nil || len(key) != ed25519.PublicKeySize || len(signed.Payload) > 16384 || len(signed.Payload) == 0 || !ed25519.Verify(key, signed.Payload, signed.Signature) {
		return invalid
	}
	p, err := marker(ctx, store, config, doc, reviewed, selected)
	if err != nil {
		return err
	}
	if !bytes.Equal(p, signed.Payload) {
		return invalid
	}
	return check(ctx, doc, selected)
}
