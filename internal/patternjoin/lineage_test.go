package patternjoin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"testing"
)

func TestSignedLineageNeedsBothCurrentFilesAndExactDocument(t *testing.T) {
	public, key, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	for _, typ := range []string{"r12", "r2000"} {
		t.Run(typ, func(t *testing.T) {
			o, c, a, b, s := fixture(t, typ)
			d := Document{Row: entity.SavedPatternRowIdentity{TechCardID: 156, ScopeKey: "main", PatternLineKey: "01JABCDEFGHJKMNPQRSTVWXYZ0", SavedRowRevision: 4, CardLockVersion: 8, URL: a.URL, SizeBytes: a.Size, SHA256: a.SHA256}, AssemblyRevision: "asm:v1:0123456789abcdef0123456789abcdef:19", CompleteSourceSHA256: digest([]byte("complete source")), FixedLineKey: "01JABCDEFGHJKMNPQRSTVWXYZD", MovingLineKey: "01JABCDEFGHJKMNPQRSTVWXYZE", ResultLineKey: "01JABCDEFGHJKMNPQRSTVWXYZB"}
			check := func(_ context.Context, doc Document, selection Selection) error {
				if doc != d || selection != s {
					return invalid
				}
				return nil
			}
			sig, err := SignNativeLineage(t.Context(), o, c, d, b, s, check, key)
			if err != nil {
				t.Fatal(err)
			}
			if err = MatchNativeLineage(t.Context(), o, c, d, b, s, check, public, sig); err != nil {
				t.Fatal(err)
			}
			denied := func(context.Context, Document, Selection) error { return invalid }
			if _, e := SignNativeLineage(t.Context(), o, c, d, b, s, denied, key); e == nil {
				t.Fatal("signed after document veto")
			}
			if e := MatchNativeLineage(t.Context(), o, c, d, b, s, denied, public, sig); e == nil {
				t.Fatal("accepted after new document veto")
			}
			for _, attack := range []string{"other-key", "modified-payload", "clock", "complete-source", "lock", "row-version", "scope", "switch-lines", "existing-result", "other-result", "old-asset", "new-asset", "native-start", "wrong-source-file", "vertex-from-another-edge", "size", "missing-key"} {
				t.Run(attack, func(t *testing.T) {
					doc, selected, asset, proof, pub := d, s, b, sig, public
					switch attack {
					case "other-key":
						pub = other
					case "modified-payload":
						proof.Payload = bytes.Replace(proof.Payload, []byte("FP"), []byte("FF"), 1)
					case "clock":
						doc.AssemblyRevision = "asm:v1:0123456789abcdef0123456789abcdef:20"
					case "complete-source":
						doc.CompleteSourceSHA256 = digest([]byte("later"))
					case "lock":
						doc.Row.CardLockVersion++
					case "row-version":
						doc.Row.SavedRowRevision++
					case "scope":
						doc.Row.ScopeKey = "lining"
					case "switch-lines":
						doc.FixedLineKey, doc.MovingLineKey = doc.MovingLineKey, doc.FixedLineKey
					case "existing-result":
						doc.ResultLineKey = doc.MovingLineKey
					case "other-result":
						doc.ResultLineKey = "01JABCDEFGHJKMNPQRSTVWXYZC"
					case "old-asset":
						doc.Row.SHA256 = asset.SHA256
					case "new-asset":
						asset.Size++
					case "native-start":
						selected.FixedInterval.Start = selected.FixedInterval.End
					case "wrong-source-file":
						selected.FixedInterval.Curve = selected.FixedInterval.Curve[1:]
					case "vertex-from-another-edge":
						selected.MovingInterval.End = selected.FixedInterval.End
					case "size":
						selected.SizeToken = "L"
					case "missing-key":
						pub = nil
					}
					if err := MatchNativeLineage(t.Context(), o, c, doc, asset, selected, check, pub, proof); err == nil {
						t.Fatal("accepted drift")
					}
				})
			}
		})
	}
}
