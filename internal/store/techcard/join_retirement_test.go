package techcard

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

const joinResult = "01JABCDEFGHJKMNPQRSTVWXYZB"
const joinLabel = "01JABCDEFGHJKMNPQRSTVWXYZC"

func joinFixture(t *testing.T) (*entity.TechCard, joinRetirement) {
	t.Helper()
	card, row, lines := completeSourceFixture()
	// The surviving referenced pieces belong to other material, so their seam source
	// fingerprints do not depend on the main row being replaced.
	card.PieceDxfAliases[2].FabricPurpose = "pocketing"
	card.PieceDxfAliases[2].BlockName = "FRONT"
	for _, l := range lines {
		card.BomItems = append(card.BomItems, entity.TechCardBomItem{LineKey: l.LineKey, Section: l.Section, Purpose: sql.NullString{String: l.Purpose, Valid: l.Purpose != ""}})
	}
	// Two new plain pieces live in the main fabric file. Two other pieces and their
	// operations, POSE, and physical seams on the same card are genuine persisted inputs.
	const fixed = "01JABCDEFGHJKMNPQRSTVWXYZD"
	const moving = "01JABCDEFGHJKMNPQRSTVWXYZE"
	for i, name := range []string{"FP", "BP"} {
		k := []string{fixed, moving}[i]
		card.Pieces = append(card.Pieces, entity.TechCardPiece{Id: i + 51, LineKey: k, Name: name, Grainline: "lengthwise", PiecesPerGarment: 1, CutSymmetry: sql.NullString{Valid: true, String: "identical"}})
		card.PieceDxfAliases = append(card.PieceDxfAliases, entity.TechCardPieceDxfAlias{PieceId: i + 51, PieceLineKey: k, FabricPurpose: "main", BlockName: name})
	}
	card.GarmentLabels = []entity.TechCardGarmentLabel{{LineKey: joinLabel, Key: "brand"}}
	src, err := buildSavedAssemblyCompleteSourceV1(card, row, lines, completeSourceClock)
	require.NoError(t, err)
	return card, joinRetirement{row: row, inputs: [2]joinLineGroup{{fixed, []string{"FP"}}, {moving, []string{"BP"}}},
		resultBlock: "JN", resultLineKey: joinResult, assemblyRevision: completeSourceClock, completeSourceSHA256: src.SourceSHA256}
}

func TestJoinRetirementRetainsEveryUnselectedAlias(t *testing.T) {
	card, j := joinFixture(t)
	want := slices.Clone(card.PieceDxfAliases[:3])
	card.PieceDxfAliases = append(card.PieceDxfAliases, entity.TechCardPieceDxfAlias{PieceId: 51, PieceLineKey: j.inputs[0].lineKey, FabricPurpose: "main", BlockName: "FP_M"})
	j.inputs[0].blockNames = append(j.inputs[0].blockNames, "FP_M")
	before := slices.Clone(card.PieceDxfAliases)
	r, err := planJoinRetirementReferences(card, entity.RollGoodsLinesOfBom(card.BomItems), j)
	require.NoError(t, err)
	require.Equal(t, [2]int{51, 52}, r.sourceIDs)
	require.Equal(t, want, r.keptAliases)
	require.Equal(t, before, card.PieceDxfAliases)
}

func TestJoinRetirementRejectsChangedStorageReferences(t *testing.T) {
	cases := []struct {
		name   string
		change func(*entity.TechCard, *joinRetirement)
	}{
		{"lock", func(c *entity.TechCard, j *joinRetirement) { c.LockVersion++ }},
		{"row revision", func(c *entity.TechCard, j *joinRetirement) { c.Patterns[1].SavedRowRevision++ }},
		{"row SHA", func(c *entity.TechCard, j *joinRetirement) { c.Patterns[1].SHA256.String = strings.Repeat("9", 64) }},
		{"scope", func(c *entity.TechCard, j *joinRetirement) { j.row.ScopeKey = "pocketing" }},
		{"two rows same scope", func(c *entity.TechCard, j *joinRetirement) { c.Patterns = append(c.Patterns, c.Patterns[1]) }},
		{"alias rebound", func(c *entity.TechCard, j *joinRetirement) { c.PieceDxfAliases[3].PieceLineKey = c.Pieces[0].LineKey }},
		{"alias different case", func(c *entity.TechCard, j *joinRetirement) { c.PieceDxfAliases[3].BlockName = "fP" }},
		{"new unreviewed group", func(c *entity.TechCard, j *joinRetirement) {
			a := c.PieceDxfAliases[3]
			a.BomLineKey = completeSourcePocketBOM
			a.FabricPurpose = ""
			c.PieceDxfAliases = append(c.PieceDxfAliases, a)
		}},
		{"case-equivalent target alias", func(c *entity.TechCard, j *joinRetirement) {
			a := c.PieceDxfAliases[3]
			a.BlockName = " jN "
			c.PieceDxfAliases = append(c.PieceDxfAliases, a)
		}},
		{"reuse input key", func(c *entity.TechCard, j *joinRetirement) { j.resultLineKey = j.inputs[1].lineKey }},
		{"reuse other piece", func(c *entity.TechCard, j *joinRetirement) { j.resultLineKey = completeSourceMainPiece }},
		{"callout", func(c *entity.TechCard, j *joinRetirement) {
			c.Pieces[2].CalloutNumber = sql.NullInt32{Int32: 3, Valid: true}
		}},
		{"fusing geometry", func(c *entity.TechCard, j *joinRetirement) {
			c.Pieces[2].FusingZone = &entity.TechCardPieceFusingZone{DxfPieceKey: "other"}
		}},
		{"operation input", func(c *entity.TechCard, j *joinRetirement) {
			c.Operations[1].InputKeys = append(c.Operations[1].InputKeys, j.inputs[1].lineKey)
		}},
		{"legacy piece FK", func(c *entity.TechCard, j *joinRetirement) { c.Operations[0].PieceIds = []int{52} }},
		{"typed input", func(c *entity.TechCard, j *joinRetirement) {
			c.Operations[0].AssemblyInputs = []entity.OperationInput{{Key: j.inputs[0].lineKey, Kind: entity.AssemblyInputPiece}}
		}},
		{"layer order", func(c *entity.TechCard, j *joinRetirement) {
			c.Operations[0].LayerOrder = []string{j.inputs[1].lineKey}
		}},
		{"fold", func(c *entity.TechCard, j *joinRetirement) {
			c.PieceFolds = []entity.TechCardPieceFold{{TechCardPieceFoldInput: entity.TechCardPieceFoldInput{PieceLineKey: j.inputs[1].lineKey}}}
		}},
		{"topstitch", func(c *entity.TechCard, j *joinRetirement) {
			c.Operations[1].TopstitchLines = []entity.TechCardTopstitchLine{{Parts: []entity.TechCardSeamAnchor{{PieceLineKey: j.inputs[0].lineKey}}}}
		}},
		{"label line key", func(c *entity.TechCard, j *joinRetirement) { j.resultLineKey = joinLabel }},
		{"annotation", func(c *entity.TechCard, j *joinRetirement) {
			c.Operations[1].Media = []entity.TechCardOperationMedia{{Annotations: []entity.TechCardAnnotation{{PieceLineKeys: []string{j.inputs[1].lineKey}}}}}
		}},
		{"material FK", func(c *entity.TechCard, j *joinRetirement) {
			c.Colorways = []entity.TechCardColorway{{Usages: []entity.TechCardColorwayUsage{{PieceId: sql.NullInt64{Int64: 51, Valid: true}}}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			card, j := joinFixture(t)
			tc.change(card, &j)
			r, err := planJoinRetirementReferences(card, entity.RollGoodsLinesOfBom(card.BomItems), j)
			require.Error(t, err)
			require.Nil(t, r)
		})
	}
}

type joinReadSQL struct {
	advancedAssembly bool
	annotations      []byte
	usage            bool
	cardLock         sync.Mutex
	lockAttempt      chan struct{}
	fakeConn
	sideA, sideB []byte
	attached     bool
}
type joinSourceConnector struct{ c *joinReadSQL }

func (d joinSourceConnector) Driver() driver.Driver                        { return fakeDriver{} }
func (d joinSourceConnector) Connect(context.Context) (driver.Conn, error) { return d.c, nil }
func (c *joinReadSQL) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.queries = append(c.queries, recordedStatement{query: q, args: args})
	switch {
	case strings.Contains(q, "FROM tech_card WHERE id = ? FOR UPDATE"):
		close(c.lockAttempt)
		c.cardLock.Lock()
		return &fakeRows{cols: []string{"lock_version", "approval_state", "approved_at", "released_at", "purpose", "stage", "measurement_unit", "create_request_id"},
			vals: [][]driver.Value{{int64(9), "draft", nil, nil, "garment", "production", "mm", nil}}}, nil
	case strings.Contains(q, "tech_card_operation_input"):
		return &fakeRows{cols: []string{"invalid"}, vals: [][]driver.Value{{false}}}, nil
	case strings.Contains(q, "tech_card_assembly_clock"):
		revision := int64(19)
		if c.advancedAssembly {
			revision++
		}
		return &fakeRows{cols: []string{"lineage_id", "revision"}, vals: [][]driver.Value{{"0123456789abcdef0123456789abcdef", revision}}}, nil
	case strings.Contains(q, "SELECT side_a, side_b FROM tech_card_seam"):
		return &fakeRows{cols: []string{"side_a", "side_b"}, vals: [][]driver.Value{{c.sideA, c.sideB}}}, nil
	case strings.Contains(q, "SELECT m.annotations"):
		anns := &fakeRows{cols: []string{"annotations"}}
		if c.annotations != nil {
			anns.vals = [][]driver.Value{{c.annotations}}
		}
		return anns, nil
	case strings.Contains(q, "tech_card_colorway_usage"):
		return &fakeRows{cols: []string{"used"}, vals: [][]driver.Value{{c.usage}}}, nil
	case strings.Contains(q, "tech_card_pattern_semantic_sidecar"):
		return &fakeRows{cols: []string{"attached"}, vals: [][]driver.Value{{c.attached}}}, nil
	default:
		return nil, fmt.Errorf("unexpected read %s", q)
	}
}

func TestJoinRetirementUsesAllDurableSeamSides(t *testing.T) {
	for _, which := range []string{"unrelated", "unowned physical", "unowned label pose", "other seam in replaced fabric", "hidden recipe", "raw annotations", "corrupt annotations", "corrupt", "partially decoded", "empty", "attached", "clock drift", "source drift"} {
		t.Run(which, func(t *testing.T) {
			card, j := joinFixture(t)
			// Change an unowned row with the same assembly clock to isolate what the digest covers.
			// Actual seam RPCs advance the assembly clock while leaving the document lock unchanged.
			row := &card.Seams[0]
			row.Stale = true
			row.Status = entity.TechCardSeamRejected
			if which == "unowned label pose" {
				row.Kind = entity.TechCardSeamKindPose
				row.SideA = []entity.TechCardSeamAnchor{{PieceLineKey: joinLabel}}
				row.SideB = []entity.TechCardSeamAnchor{{PieceLineKey: j.inputs[1].lineKey}}
			}
			if which == "unowned physical" {
				row.SideA = []entity.TechCardSeamAnchor{{PieceLineKey: j.inputs[0].lineKey}}
			}
			if which == "other seam in replaced fabric" {
				card.PieceDxfAliases[2].FabricPurpose = "main"
				s, err := buildSavedAssemblyCompleteSourceV1(card, j.row, entity.RollGoodsLinesOfBom(card.BomItems), j.assemblyRevision)
				require.NoError(t, err)
				j.completeSourceSHA256 = s.SourceSHA256
			}
			hash, err := buildCheckedSavedAssemblyCompleteSourceV1(card, j.row, entity.RollGoodsLinesOfBom(card.BomItems), j.assemblyRevision, j.completeSourceSHA256)
			require.NoError(t, err)
			require.Equal(t, j.completeSourceSHA256, hash.SourceSHA256)
			c := &joinReadSQL{sideA: []byte(fmt.Sprintf(`[{"piece_line_key":%q}]`, row.SideA[0].PieceLineKey)), sideB: []byte(fmt.Sprintf(`[{"piece_line_key":%q}]`, row.SideB[0].PieceLineKey))}
			switch which {
			case "raw annotations":
				c.annotations = []byte(fmt.Sprintf(`[{"pieces":[%q]}]`, j.inputs[1].lineKey))
			case "corrupt annotations":
				c.annotations = []byte("{")
			case "hidden recipe":
				c.usage = true
			case "corrupt":
				c.sideB = []byte("{")
			case "partially decoded":
				c.sideA = []byte(`[{},{"piece_line_key":"truncated"}]`)
			case "empty":
				c.sideB = []byte("[]")
			case "attached":
				c.attached = true
			case "clock drift":
				j.assemblyRevision = strings.Replace(j.assemblyRevision, ":19", ":20", 1)
			case "source drift":
				j.completeSourceSHA256 = strings.Repeat("f", 64)
			}
			db := sqlx.NewDb(sql.OpenDB(joinSourceConnector{c}), "mysql")
			defer db.Close()
			r, err := readJoinRetirementUnderLock(context.Background(), guardedRenameRepo{db: db, cards: guardedRenameCardReader{card: card}}, j)
			if which == "unrelated" {
				require.NoError(t, err)
				require.Equal(t, card.PieceDxfAliases[:3], r.keptAliases)
			} else {
				require.Error(t, err)
				require.Nil(t, r)
			}
			require.Empty(t, c.execs)
		})
	}
}

// Enter through the real document transaction and pause before any ordinary write. The
// snapshot callback is a test observation point after SELECT FOR UPDATE. It is not the
// proposed way to dispatch a Join or a way for ordinary clients to install a predicate.
func TestJoinRetirementAfterExistingDocumentLock(t *testing.T) {
	for _, race := range []string{"unowned label placement", "versionless attachment"} {
		t.Run(race, func(t *testing.T) {
			card, j := joinFixture(t)
			ctx := context.Background()
			c := &joinReadSQL{lockAttempt: make(chan struct{}), sideA: []byte(fmt.Sprintf(`[{"piece_line_key":%q}]`, joinLabel)), sideB: []byte(fmt.Sprintf(`[{"piece_line_key":%q}]`, completeSourceOtherPiece))}
			c.cardLock.Lock()
			go func() {
				<-c.lockAttempt
				if race == "versionless attachment" {
					c.attached = true
				} else {
					c.advancedAssembly = true
					c.sideB = []byte(fmt.Sprintf(`[{"piece_line_key":%q}]`, j.inputs[0].lineKey))
				}
				c.cardLock.Unlock()
			}()
			db := sqlx.NewDb(sql.OpenDB(joinSourceConnector{c}), "mysql")
			defer db.Close()
			rep := guardedRenameRepo{db: db, cards: guardedRenameCardReader{card: card}}
			calls := 0
			_, err := (&Store{}).UpdateTechCardTx(ctx, rep, card.Id, &entity.TechCardInsert{SnapshotReason: "checkpoint", BuildSnapshotFromLockedCard: func(*entity.TechCard) (string, error) {
				calls++
				if r, e := readJoinRetirementUnderLock(ctx, rep, j); e != nil {
					return "", e
				} else if r != nil {
					t.Error("late protected dependency was accepted")
				}
				return "", errors.New("unexpected successful retirement verification")
			}}, card.LockVersion)
			c.cardLock.Unlock()
			if race == "unowned label placement" {
				require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyMoved)
			} else {
				require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyUnsupported)
			}
			require.Equal(t, 1, calls)
			require.Empty(t, c.execs)
			require.Contains(t, c.queries[0].query, "FOR UPDATE")
		})
	}
}

func TestJoinRetirementVerifiesResultLineDelta(t *testing.T) {
	for _, which := range []string{"exact reviewed replacement", "ordinary additive Apply", "retain source alias", "reuse source line",
		"reuse retired numeric id", "result alias owner", "unrelated alias move", "unrelated piece rename", "wrong document clock"} {
		t.Run(which, func(t *testing.T) {
			before, j := joinFixture(t)
			before.Pieces[0].Id = 31
			before.Pieces[1].Id = 32
			plan, err := planJoinRetirementReferences(before, entity.RollGoodsLinesOfBom(before.BomItems), j)
			require.NoError(t, err)
			after := *before
			after.LockVersion++
			child := before.Pieces[2]
			child.LineKey = j.resultLineKey
			child.Name = j.resultBlock
			child.Id = 71
			after.Pieces = append(slices.Clone(before.Pieces[:2]), child)
			reviewed := entity.TechCardPieceDxfAlias{FabricPurpose: "main", PieceLineKey: j.resultLineKey, BlockName: j.resultBlock}
			live := reviewed
			live.PieceId = child.Id
			after.PieceDxfAliases = append(slices.Clone(plan.keptAliases), live)
			switch which {
			case "ordinary additive Apply":
				after.Pieces = append(slices.Clone(before.Pieces), child)
				after.PieceDxfAliases = append(slices.Clone(before.PieceDxfAliases), live)
			case "retain source alias":
				after.PieceDxfAliases = append(after.PieceDxfAliases, before.PieceDxfAliases[3])
			case "reuse source line":
				after.Pieces[2].LineKey = j.inputs[0].lineKey
			case "reuse retired numeric id":
				after.Pieces[2].Id = before.Pieces[2].Id
			case "result alias owner":
				after.PieceDxfAliases[3].PieceLineKey = j.inputs[1].lineKey
			case "unrelated alias move":
				after.PieceDxfAliases[1].BomLineKey = completeSourceFusingBOM
			case "unrelated piece rename":
				after.Pieces[1].Name = "unexpected"
			case "wrong document clock":
				after.LockVersion++
			}
			err = verifyJoinLineDelta(before, &after, j, plan, reviewed)
			if which == "exact reviewed replacement" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
