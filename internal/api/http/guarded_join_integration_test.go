package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/patternjoin"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/testdbguard"
	pb "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestGuardedNativeJoinGatewayMySQL(t *testing.T) {
	dsn := os.Getenv("AC17_JOIN_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set AC17_JOIN_TEST_MYSQL_DSN to a disposable loopback MySQL database")
	}
	require.NoError(t, testdbguard.CheckMySQLDSN(dsn))
	cfg, e := mysql.ParseDSN(dsn)
	require.NoError(t, e)
	host, _, e := net.SplitHostPort(cfg.Addr)
	require.NoError(t, e)
	require.True(t, net.ParseIP(host).IsLoopback())
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	repository, e := store.NewForTest(ctx, store.Config{DSN: dsn, Automigrate: true, MaxOpenConnections: 10, MaxIdleConnections: 5})
	require.NoError(t, e)
	t.Cleanup(repository.Close)
	works, e := repository.TechCards().GetOperationWorkCatalog(ctx)
	require.NoError(t, e)
	entity.SetOperationWorkCatalog(works)
	t.Cleanup(func() { entity.SetOperationWorkCatalog(nil) })
	const cdn = "ac17-join.invalid"
	dto.SetManagedPatternHosts(cdn)
	t.Cleanup(func() { dto.SetManagedPatternHosts() })
	const rowKey = "01JABCDEFGHJKMNPQRSTVWXYZ0"
	const bom = "01JABCDEFGHJKMNPQRSTVWXYZ8"
	const result = "01JABCDEFGHJKMNPQRSTVWXYZB"
	for _, dialect := range []string{"r12", "r2000"} {
		t.Run(dialect, func(t *testing.T) {
			files := map[string][]byte{}
			asset := func(part string) patternjoin.Asset {
				raw, e := os.ReadFile("../../patternjoin/testdata/native-join-" + dialect + "-" + part + ".dxf")
				require.NoError(t, e)
				k := "tech-card-patterns/" + part + ".dxf"
				files[k] = raw
				h := sha256.Sum256(raw)
				return patternjoin.Asset{URL: "https://" + cdn + "/" + k, Size: int64(len(raw)), SHA256: hex.EncodeToString(h[:])}
			}
			original, reviewed := asset("source"), asset("child")
			pocket := asset("pocket")
			s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p, ok := files[strings.TrimPrefix(r.URL.Path, "/patterns/")]
				if !ok || r.Method != "GET" && r.Method != "HEAD" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("ETag", `"abcdefghijklmnopqrstuvwxyz012345"`)
				w.Header().Set("Last-Modified", "Wed, 18 Nov 2026 07:00:00 GMT")
				w.Header().Set("Content-Length", fmt.Sprint(len(p)))
				if r.Method == "GET" {
					w.Write(p)
				}
			}))
			t.Cleanup(s3.Close)
			u, _ := url.Parse(s3.URL)
			client, e := minio.New(u.Host, &minio.Options{Region: "us-east-1", BucketLookup: minio.BucketLookupPath})
			require.NoError(t, e)
			managed := &bucket.Bucket{Client: client, Config: &bucket.Config{S3BucketName: "patterns", SubdomainEndpoint: cdn}}
			gateway := newGuardedPatternGateway(t, ctx, repository, managed)
			raw, e := os.ReadFile("../../patternjoin/testdata/native-join-" + dialect + "-intent.json")
			require.NoError(t, e)
			type edge struct{ PieceID, PieceLineKey, SourceCurveID, StartVertexID, EndVertexID string }
			var intent struct {
				Fixed, Moving     edge
				TranslateMovingMm struct{ X, Y int64 }
			}
			require.NoError(t, json.Unmarshal(raw, &intent))
			// The real Wizard fixtures and source-curve IDs bind techCardId 156. Reserve this exact
			// unused slot, never alter the identity or take over a card that was already present.
			const cardID = 156
			_, e = repository.DB().ExecContext(ctx, "INSERT INTO tech_card (id,name) VALUES (?,?)", cardID, "AC17 native join fixture")
			require.NoError(t, e)
			t.Cleanup(func() {
				_, e := repository.DB().ExecContext(context.Background(), "DELETE FROM tech_card_operation WHERE tech_card_id=?", cardID)
				require.NoError(t, e)
				_, err := repository.DB().ExecContext(context.Background(), "DELETE FROM tech_card WHERE id=?", cardID)
				require.NoError(t, err)
			})
			ns := func(s string) sql.NullString { return sql.NullString{Valid: true, String: s} }
			seed := &entity.TechCardInsert{SizeIds: []int{4, 5}, Name: "AC17 native join fixture", StyleNumber: ns("AC17-JOIN"), Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft, MeasurementUnit: entity.TechCardUnitMm,
				BomItems: []entity.TechCardBomItem{{LineKey: bom, Section: entity.BomSectionFabric, Name: "Shell", Purpose: ns("main")}}, PieceDxfAliasesSet: true,
				Patterns: []entity.TechCardSizePattern{{LineKey: rowKey, BomLineKey: ns(bom), FabricPurpose: ns("main"), URL: original.URL, SizeBytes: sql.NullInt64{Int64: original.Size, Valid: true}, SHA256: ns(original.SHA256), Filename: ns("original.dxf")}}}
			for _, p := range []edge{intent.Fixed, intent.Moving} {
				seed.Pieces = append(seed.Pieces, entity.TechCardPiece{LineKey: p.PieceLineKey, Name: p.PieceID, PiecesPerGarment: 1, Grainline: "lengthwise", CutSymmetry: ns("identical")})
				seed.PieceDxfAliases = append(seed.PieceDxfAliases, entity.TechCardPieceDxfAlias{BomLineKey: bom, FabricPurpose: "main", BlockName: p.PieceID, PieceLineKey: p.PieceLineKey})
			}
			const labelKey = "01JABCDEFGHJKMNPQRSTVWXYZC"
			seed.LabelsAware = true
			seed.GarmentLabels = []entity.TechCardGarmentLabel{{Key: "brand", LineKey: labelKey}}
			const pocketKey = "01JABCDEFGHJKMNPQRSTVWXYZP"
			const pocketBOM = "01JABCDEFGHJKMNPQRSTVWXYZ1"
			const pocketRow = "01JABCDEFGHJKMNPQRSTVWXYZQ"
			seed.BomItems = append(seed.BomItems, entity.TechCardBomItem{LineKey: pocketBOM, Section: entity.BomSectionFabric, Name: "Pocket fabric", Purpose: ns("pocketing")})
			seed.Patterns = append(seed.Patterns, entity.TechCardSizePattern{LineKey: pocketRow, BomLineKey: ns(pocketBOM), FabricPurpose: ns("pocketing"), SizeId: 5, URL: pocket.URL, SizeBytes: sql.NullInt64{Valid: true, Int64: pocket.Size}, SHA256: ns(pocket.SHA256), Filename: ns("pocket_L.dxf")})
			seed.Pieces = append(seed.Pieces, entity.TechCardPiece{LineKey: pocketKey, Name: "Pocket", PiecesPerGarment: 1, Grainline: "lengthwise", CutSymmetry: ns("identical")})
			seed.PieceDxfAliases = append(seed.PieceDxfAliases, entity.TechCardPieceDxfAlias{FabricPurpose: "pocketing", BomLineKey: pocketBOM, BlockName: "PK_L", PieceLineKey: pocketKey})
			seed.Operations = []entity.TechCardOperation{{OperationNumber: sql.NullInt32{Int32: 10, Valid: true}, OperationType: entity.OpTypePress, PressEquipment: ns("iron"), Zone: entity.ZoneOther, Note: ns("Press the pocket"), Work: ns("press_flat"), InputKeys: []string{pocketKey}, PieceLineKeys: []string{pocketKey}}}
			require.NoError(t, repository.TechCards().UpdateTechCard(ctx, cardID, seed, 0))
			before := readGuardedRenameGatewayCard(t, gateway, cardID)
			findPattern := func(c *common.TechCard, k string) *common.TechCardSizePattern {
				for _, p := range c.GetTechCard().GetPatterns() {
					if p.GetLineKey() == k {
						return p
					}
				}
				t.Fatalf("missing pattern %s", k)
				return nil
			}
			r := findPattern(before, rowKey)
			lock := before.GetLockVersion()
			row := entity.SavedPatternRowIdentity{TechCardID: cardID, ScopeKey: "main", PatternLineKey: rowKey, SavedRowRevision: r.GetSavedRowRevision(), CardLockVersion: int(lock), URL: original.URL, SizeBytes: original.Size, SHA256: original.SHA256, PatternVersion: int(r.GetVersion())}
			snap, e := repository.TechCards().GetTechCardSavedAssemblySnapshot(ctx, entity.SavedAssemblySnapshotRequest{ExpectedRow: row, Profile: entity.SavedAssemblySnapshotCompleteSourceV1})
			require.NoError(t, e)
			pick := func(e edge) *pb.GuardedNativePieceJoin_Boundary {
				return &pb.GuardedNativePieceJoin_Boundary{PieceLineKey: e.PieceLineKey, PieceIdentity: e.PieceID, CurveId: e.SourceCurveID, StartVertexId: e.StartVertexID, EndVertexId: e.EndVertexID}
			}
			join := &pb.GuardedNativePieceJoin{ExpectedRow: &pb.TechCardSavedPatternRowIdentity{TechCardId: cardID, ScopeKey: "main", PatternLineKey: rowKey, SavedRowRevision: r.GetSavedRowRevision(), CardLockVersion: lock, Url: original.URL, SizeBytes: original.Size, Sha256: original.SHA256, PatternVersion: r.GetVersion()}, ExpectedAssemblyRevision: snap.ServerAssemblyRevision, ExpectedCompleteSourceSha256: snap.CompleteSourceV1.SourceSHA256, Fixed: pick(intent.Fixed), Moving: pick(intent.Moving), ResultLineKey: result, ResultIdentity: "JN", FabricPurpose: "main", BomLineKey: bom, SizeId: 4, SizeToken: "M", TranslateXMillionthsMm: 1e6 * intent.TranslateMovingMm.X, TranslateYMillionthsMm: 1e6 * intent.TranslateMovingMm.Y, ReviewedUrl: reviewed.URL, ReviewedSizeBytes: reviewed.Size, ReviewedSha256: reviewed.SHA256, ReviewedFilename: "joined.dxf"}
			req := &pb.UpdateTechCardRequest{Id: cardID, ExpectedLockVersion: lock, GuardedPieceJoin: join}
			post := func(r *pb.UpdateTechCardRequest) (int, string) {
				code, body := guardedRenameHTTP(t, gateway, http.MethodPost, "/api/admin/tech-card/update", r)
				return code, string(body)
			}
			bad := proto.Clone(req).(*pb.UpdateTechCardRequest)
			bad.GuardedPieceJoin.Moving.EndVertexId = join.Fixed.EndVertexId
			code, reply := post(bad)
			require.Equal(t, http.StatusBadRequest, code, reply)
			require.True(t, proto.Equal(before, readGuardedRenameGatewayCard(t, gateway, cardID)))
			type clocks struct{ Assembly, Journal, Snapshot, History int }
			readClocks := func() clocks {
				var v clocks
				e := repository.DB().GetContext(ctx, &v, `SELECT revision AS assembly,(SELECT COUNT(*) FROM tech_card_revision WHERE tech_card_id=156) AS journal,(SELECT COUNT(*) FROM tech_card_snapshot WHERE tech_card_id=156) AS snapshot,(SELECT COUNT(*) FROM tech_card_size_pattern_history WHERE tech_card_id=156) AS history FROM tech_card_assembly_clock WHERE tech_card_id=156`)
				require.NoError(t, e)
				return v
			}
			unchangedClocks := readClocks()
			for _, fail := range []string{"unowned POSE", "same rank wrong dictionary", "mixed full body", "sidecar on untouched rank", "after child INSERT"} {
				t.Run(fail, func(t *testing.T) {
					var sidecarRoute string
					var attachment *pb.PutTechCardPatternSemanticSidecarRequest
					request := proto.Clone(req).(*pb.UpdateTechCardRequest)
					switch fail {
					case "same rank wrong dictionary":
						request.GuardedPieceJoin.SizeId = 5
					case "mixed full body":
						request.TechCard = proto.Clone(before.GetTechCard()).(*common.TechCardInsert)
					case "unowned POSE":
						_, e := repository.DB().ExecContext(ctx, `INSERT INTO tech_card_seam (tech_card_id,seam_key,status,kind,source,side_a,side_b,source_fingerprint)VALUES(?,?,'rejected','pose','manual',?,?,?)`, cardID, "01JABCDEFGHJKMNPQRSTVWXYZW", fmt.Sprintf(`[{"piece_line_key":%q,"samples":[{"u":0,"v":0},{"u":0.2,"v":0},{"u":0.4,"v":0},{"u":0.6,"v":0},{"u":0.8,"v":0}],"perim_share":0.2,"len_mm":20}]`, labelKey), fmt.Sprintf(`[{"piece_line_key":%q,"samples":[{"u":1,"v":0},{"u":0.8,"v":0},{"u":0.6,"v":0},{"u":0.4,"v":0},{"u":0.2,"v":0}],"perim_share":0.2,"len_mm":20}]`, intent.Moving.PieceLineKey), strings.Repeat("c", 64))
						require.NoError(t, e)
						t.Cleanup(func() {
							_, e := repository.DB().ExecContext(ctx, "DELETE FROM tech_card_seam WHERE tech_card_id=? AND seam_key=?", cardID, "01JABCDEFGHJKMNPQRSTVWXYZW")
							require.NoError(t, e)
						})
					case "sidecar on untouched rank":
						route, attach := guardedRenameV1Request(before, pocketRow)
						sidecarRoute, attachment = route, attach
						code, reply := guardedRenameHTTP(t, gateway, http.MethodPut, route, attach)
						require.Equal(t, 200, code, string(reply))
						t.Cleanup(func() {
							_, e := repository.DB().ExecContext(ctx, `DELETE sidecar FROM tech_card_pattern_semantic_sidecar sidecar JOIN tech_card_size_pattern p ON p.id=sidecar.pattern_row_id WHERE p.tech_card_id=? AND p.line_key=?`, cardID, pocketRow)
							require.NoError(t, e)
						})
					case "after child INSERT":
						_, e := repository.DB().ExecContext(ctx, `CREATE TRIGGER ac17_join_corrupt_child BEFORE INSERT ON tech_card_piece FOR EACH ROW BEGIN IF NEW.tech_card_id=156 AND NEW.line_key='01JABCDEFGHJKMNPQRSTVWXYZB' THEN SET @ac17_join_probe='`+dialect+`'; SET NEW.name='Altered joined piece'; END IF; END`)
						require.NoError(t, e)
						t.Cleanup(func() {
							_, e := repository.DB().ExecContext(ctx, "DROP TRIGGER ac17_join_corrupt_child")
							require.NoError(t, e)
						})
					}
					baseline := readGuardedRenameGatewayCard(t, gateway, cardID)
					code, reply := post(request)
					require.Equal(t, 400, code, reply)
					require.True(t, proto.Equal(baseline, readGuardedRenameGatewayCard(t, gateway, cardID)))
					require.Equal(t, unchangedClocks, readClocks())
					if fail == "after child INSERT" {
						var reached int
						require.NoError(t, repository.DB().GetContext(ctx, &reached, "SELECT COUNT(*) FROM performance_schema.user_variables_by_thread WHERE VARIABLE_NAME='ac17_join_probe' AND CONVERT(VARIABLE_VALUE USING utf8mb4)=?", dialect))
						require.Positive(t, reached, "actual INSERT was reached before the rollback")
					}
					if attachment != nil {
						c, msg := guardedRenameHTTP(t, gateway, http.MethodGet, sidecarRoute, nil)
						require.Equal(t, 200, c, string(msg))
						assertGuardedRenameV1Read(t, attachment, msg)
					}

				})
			}
			code, reply = post(req)
			require.Equal(t, 200, code, reply)
			after := readGuardedRenameGatewayCard(t, gateway, cardID)
			require.Equal(t, lock+1, after.GetLockVersion())
			require.Len(t, after.GetTechCard().GetOperations(), 1)
			require.True(t, proto.Equal(before.GetTechCard().GetOperations()[0], after.GetTechCard().GetOperations()[0]))
			unchanged := findPattern(after, pocketRow)
			oldPocket := proto.Clone(findPattern(before, pocketRow)).(*common.TechCardSizePattern)
			oldPocket.SavedRowRevision++
			require.True(t, proto.Equal(oldPocket, unchanged))
			require.Equal(t, pocket.SHA256, unchanged.GetSha256())
			require.Equal(t, int32(5), unchanged.GetSizeId())
			v := after.GetTechCard()
			require.Len(t, v.GetPieces(), 2)
			pieces := map[string]string{}
			for _, p := range v.Pieces {
				pieces[p.LineKey] = p.Name
			}
			require.Equal(t, map[string]string{result: "JN", pocketKey: "Pocket"}, pieces)
			require.Len(t, v.PieceDxfAliases.Items, 2)
			aliases := map[string]string{}
			for _, a := range v.PieceDxfAliases.Items {
				aliases[a.BlockName] = a.PieceLineKey
			}
			require.Equal(t, map[string]string{"JN": result, "PK_L": pocketKey}, aliases)
			next := findPattern(after, rowKey)
			require.Equal(t, rowKey, next.GetLineKey())
			require.Equal(t, reviewed.SHA256, next.GetSha256())
			require.Equal(t, r.GetSavedRowRevision()+1, next.GetSavedRowRevision())
			require.Equal(t, reviewed.URL, next.GetUrl())
			require.Len(t, next.GetHistory(), 1)
			require.Equal(t, original.URL, next.GetHistory()[0].GetUrl())
			require.Equal(t, original.SHA256, next.GetHistory()[0].GetSha256())
			signedEdge := func(e edge) patternjoin.DirectedEdge {
				return patternjoin.DirectedEdge{Curve: e.SourceCurveID, Start: e.StartVertexID, End: e.EndVertexID}
			}
			selected := patternjoin.Selection{FixedInterval: signedEdge(intent.Fixed), MovingInterval: signedEdge(intent.Moving), CardID: cardID, FabricPurpose: "main", BOMLineKey: bom, SizeToken: "M", SizeID: 4, Fixed: "FP", Moving: "BP", Result: "JN", FixedEdge: 1, MovingEdge: 3, TranslateX: join.TranslateXMillionthsMm, TranslateY: join.TranslateYMillionthsMm}
			binding, e := patternjoin.ReadBinding(ctx, managed, managed.Config, original, patternjoin.Asset{URL: next.Url, Size: next.SizeBytes, SHA256: next.GetSha256()}, selected)
			require.NoError(t, e)
			require.Equal(t, reviewed, binding.Reviewed)
			code, _ = post(req)
			require.Equal(t, 409, code)
			require.True(t, proto.Equal(after, readGuardedRenameGatewayCard(t, gateway, cardID)))
			t.Logf("%s lock=%d→%d revision=%d→%d original=%s child=%s lines=%s,%s→%s", dialect, lock, after.GetLockVersion(), r.GetSavedRowRevision(), next.SavedRowRevision, original.SHA256, reviewed.SHA256, intent.Fixed.PieceLineKey, intent.Moving.PieceLineKey, result)
		})
	}
}
