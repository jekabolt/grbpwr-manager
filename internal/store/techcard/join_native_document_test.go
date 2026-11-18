package techcard

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/patternjoin"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
	"io"
	"os"
	"testing"
)

func TestNativeJoinBindsSignedIdentitiesToLockedAliases(t *testing.T) {
	for _, mode := range []string{"current", "extra-rank", "swapped", "material", "other-base", "scope", "one-source", "cross-card", "lock"} {
		t.Run(mode, func(t *testing.T) {
			card, j := joinFixture(t)
			doc := patternjoin.Document{Row: j.row, AssemblyRevision: j.assemblyRevision, CompleteSourceSHA256: j.completeSourceSHA256, FixedLineKey: j.inputs[0].lineKey, MovingLineKey: j.inputs[1].lineKey, ResultLineKey: j.resultLineKey}
			s := patternjoin.Selection{CardID: card.Id, BOMLineKey: completeSourceMainBOM, FabricPurpose: "main", SizeToken: "M", Fixed: "FP", Moving: "BP", Result: "JN"}
			last := card.PieceDxfAliases[len(card.PieceDxfAliases)-1]
			last.BlockName = "BP_M"
			card.PieceDxfAliases = append(card.PieceDxfAliases, last)
			switch mode {
			case "extra-rank":
				card.PieceDxfAliases[len(card.PieceDxfAliases)-1].BlockName = "BP_L"
			case "swapped":
				s.Fixed, s.Moving = s.Moving, s.Fixed
			case "material":
				s.BOMLineKey = completeSourcePocketBOM
			case "other-base":
				s.Moving = "BK"
			case "scope":
				doc.Row.ScopeKey = "pocket"
			case "one-source":
				doc.FixedLineKey = doc.MovingLineKey
			case "cross-card":
				s.CardID++
			case "lock":
				doc.Row.CardLockVersion++
			}
			actual, err := nativeJoinRetirement(card, doc, s)
			if mode != "current" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{"FP"}, actual.inputs[0].blockNames)
			require.Equal(t, []string{"BP", "BP_M"}, actual.inputs[1].blockNames)
			_, err = planJoinRetirementReferences(card, entity.RollGoodsLinesOfBom(card.BomItems), actual)
			require.NoError(t, err)
		})
	}
}

type joinNativeObjects map[string][]byte

func (o joinNativeObjects) GetManagedObject(_ context.Context, key string) (io.ReadCloser, int64, error) {
	data, ok := o[key]
	if !ok {
		return nil, 0, fmt.Errorf("missing test asset")
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}
func TestSignedNativeLineageAtOrdinaryDocumentLock(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	for _, dialect := range []string{"r12", "r2000"} {
		for _, mode := range []string{"clear", "durable-pose", "swapped-lines", "after-capture"} {
			t.Run(dialect+"/"+mode, func(t *testing.T) {
				card, j := joinFixture(t)
				o := joinNativeObjects{}
				load := func(part string) patternjoin.Asset {
					raw, e := os.ReadFile("../../patternjoin/testdata/native-join-" + dialect + "-" + part + ".dxf")
					require.NoError(t, e)
					k := "tech-card-patterns/" + part + ".dxf"
					o[k] = raw
					hash := sha256.Sum256(raw)
					return patternjoin.Asset{URL: "https://patterns.example/" + k, Size: int64(len(raw)), SHA256: hex.EncodeToString(hash[:])}
				}
				a, b := load("source"), load("child")
				raw, e := os.ReadFile("../../patternjoin/testdata/native-join-" + dialect + "-intent.json")
				require.NoError(t, e)
				type edge struct{ PieceID, PieceLineKey, SourceCurveID, StartVertexID, EndVertexID string }
				var intent struct {
					Fixed, Moving          edge
					TranslateMovingMm      struct{ X, Y int64 }
					NewIdentity, SizeToken string
					Row                    struct{ PatternLineKey, SHA256 string }
				}
				require.NoError(t, json.Unmarshal(raw, &intent))
				require.Equal(t, j.row.PatternLineKey, intent.Row.PatternLineKey)
				require.Equal(t, a.SHA256, intent.Row.SHA256)
				nativeEdge := func(e edge) patternjoin.DirectedEdge {
					return patternjoin.DirectedEdge{Curve: e.SourceCurveID, Start: e.StartVertexID, End: e.EndVertexID}
				}
				picked := patternjoin.Selection{FixedInterval: nativeEdge(intent.Fixed), MovingInterval: nativeEdge(intent.Moving), CardID: card.Id, FabricPurpose: "main", BOMLineKey: completeSourceMainBOM, SizeToken: intent.SizeToken, SizeID: 7, Fixed: intent.Fixed.PieceID, Moving: intent.Moving.PieceID, Result: intent.NewIdentity, FixedEdge: 1, MovingEdge: 3, TranslateX: 1e6 * intent.TranslateMovingMm.X, TranslateY: 1e6 * intent.TranslateMovingMm.Y}
				j.row.URL, j.row.SizeBytes, j.row.SHA256 = a.URL, a.Size, a.SHA256
				for i := range card.Patterns {
					p := &card.Patterns[i]
					if p.LineKey == j.row.PatternLineKey {
						p.URL = a.URL
						p.SizeBytes.Int64 = a.Size
						p.SHA256.String = a.SHA256
					}
				}
				fresh, e := buildSavedAssemblyCompleteSourceV1(card, j.row, entity.RollGoodsLinesOfBom(card.BomItems), j.assemblyRevision)
				require.NoError(t, e)
				doc := patternjoin.Document{Row: j.row, AssemblyRevision: j.assemblyRevision, CompleteSourceSHA256: fresh.SourceSHA256, FixedLineKey: intent.Fixed.PieceLineKey, MovingLineKey: intent.Moving.PieceLineKey, ResultLineKey: j.resultLineKey}
				c := &joinReadSQL{lockAttempt: make(chan struct{}), sideA: []byte(fmt.Sprintf(`[{"piece_line_key":%q}]`, joinLabel)), sideB: []byte(fmt.Sprintf(`[{"piece_line_key":%q}]`, completeSourceOtherPiece))}
				if mode == "durable-pose" {
					c.sideB = []byte(fmt.Sprintf(`[{"piece_line_key":%q}]`, doc.MovingLineKey))
				}
				if mode == "swapped-lines" {
					doc.FixedLineKey, doc.MovingLineKey = doc.MovingLineKey, doc.FixedLineKey
				}
				db := sqlx.NewDb(sql.OpenDB(joinSourceConnector{c}), "mysql")
				defer db.Close()
				rep := guardedRenameRepo{db: db, cards: guardedRenameCardReader{card: card}}
				cfg := &bucket.Config{SubdomainEndpoint: "patterns.example"}
				stop := errors.New("end of read-only checkpoint")
				captured := false
				_, err := (&Store{}).UpdateTechCardTx(t.Context(), rep, card.Id, &entity.TechCardInsert{SnapshotReason: "native lineage proof", BuildSnapshotFromLockedCard: func(*entity.TechCard) (string, error) {
					check := func(ctx context.Context, d patternjoin.Document, s patternjoin.Selection) error {
						return checkNativeJoinDocumentUnderLock(ctx, rep, d, s)
					}
					signature, e := patternjoin.SignNativeLineage(t.Context(), o, cfg, doc, b, picked, check, private)
					if e != nil {
						return "", e
					}
					captured = true
					if mode == "after-capture" {
						c.attached = true
					}
					if e = patternjoin.MatchNativeLineage(t.Context(), o, cfg, doc, b, picked, check, pub, signature); e != nil {
						return "", e
					}
					return "", stop
				}}, card.LockVersion)
				c.cardLock.Unlock()
				if mode == "clear" {
					require.ErrorIs(t, err, stop)
					require.True(t, captured)
				} else {
					require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyUnsupported)
					require.Equal(t, mode == "after-capture", captured)
				}
				require.Empty(t, c.execs)
				require.Contains(t, c.queries[0].query, "FOR UPDATE")
			})
		}
	}
}
