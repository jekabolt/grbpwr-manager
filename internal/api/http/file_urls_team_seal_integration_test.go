package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/jpk"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/internal/testdbguard"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	x13TeamFileKey = "0123456789abcdef0123456789abcdef"
	x13Host        = "lekalo-x13.fra1.digitaloceanspaces.com"
	x13MainRow     = "01JABCDEFGHJKMNPQRSTVWXYZ0"
	x13PocketRow   = "01JABCDEFGHJKMNPQRSTVWXYZ1"
	x13MainPiece   = "01JABCDEFGHJKMNPQRSTVWXYZ2"
	x13PocketPiece = "01JABCDEFGHJKMNPQRSTVWXYZ3"
	x13Stitch      = "01JABCDEFGHJKMNPQRSTVWXYZ4"
	x13Pose        = "01JABCDEFGHJKMNPQRSTVWXYZ5"
)

func x13TeamFileURL(name string) string {
	return "https://" + x13Host + "/lekalo-x13/t/" + x13TeamFileKey + "/lekalo-x13/2026/november/" + name
}

// TestFileURLsTeamSealsMySQL: a team other than #1, the real file url interceptor and a real bucket
// signer in front of the real handlers and MySQL store. The card's media come back signed, while the
// saved DXF row urls stay the stored string, so the complete-source seal verifies the way the client
// verifies it and a V8 sidecar built from the response (its row bindings carry the url) is accepted,
// then read back through the same saved Assembly read (lane X13 review MAJOR).
func TestFileURLsTeamSealsMySQL(t *testing.T) {
	dsn := os.Getenv("X13_FILE_URLS_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set X13_FILE_URLS_TEST_MYSQL_DSN to a disposable loopback MySQL database")
	}
	require.NoError(t, testdbguard.CheckMySQLDSN(dsn))
	mc, err := mysql.ParseDSN(dsn)
	require.NoError(t, err)
	host, _, err := net.SplitHostPort(mc.Addr)
	require.NoError(t, err)
	require.True(t, net.ParseIP(host).IsLoopback(), "use a numeric loopback host")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	repository, err := store.NewForTest(ctx, store.Config{DSN: dsn, Automigrate: true, MaxOpenConnections: 10, MaxIdleConnections: 5})
	require.NoError(t, err)
	t.Cleanup(repository.Close)
	works, err := repository.TechCards().GetOperationWorkCatalog(ctx)
	require.NoError(t, err)
	entity.SetOperationWorkCatalog(works)
	t.Cleanup(func() { entity.SetOperationWorkCatalog(nil) })
	dto.SetManagedPatternHosts(x13Host)
	t.Cleanup(func() { dto.SetManagedPatternHosts() })

	// The real signer, offline: region set, so presigning never asks the network.
	cli, err := minio.New("fra1.digitaloceanspaces.com", &minio.Options{
		Creds: credentials.NewStaticV4("probe", "probe12345", ""), Secure: true, Region: "fra1",
	})
	require.NoError(t, err)
	files := &bucket.Bucket{Client: cli, Config: &bucket.Config{
		S3AccessKey: "probe", S3SecretAccessKey: "probe12345", S3Endpoint: "fra1.digitaloceanspaces.com",
		S3BucketName: "lekalo-x13", BaseFolder: "lekalo-x13", SubdomainEndpoint: x13Host, TeamsMode: true,
	}}
	// Team #7 on the DSN database (team #1's database is on the routing allowlist).
	team := tenant.Team{ID: 7, Slug: "x13", Name: "x13", DBName: mc.DBName, FileKey: x13TeamFileKey, Status: tenant.StatusActive}
	teamCtx := tenant.With(ctx, team)

	cardID, sizeID, sizeToken, mainURL := seedX13TeamCard(t, ctx, repository)
	gateway := newX13FileURLGateway(t, ctx, repository, files, team)

	// GetTechCard: the media are signed (the interceptor is on), the pattern urls are the stored ones.
	card := readGuardedRenameGatewayCard(t, gateway, int32(cardID))
	media := card.GetResolvedTechnicalMedia()
	require.Len(t, media, 1)
	require.Contains(t, media[0].GetMedia().GetMedia().GetFullSize().GetMediaUrl(), "X-Amz-Signature=")
	main := guardedRenamePattern(card, x13MainRow)
	require.NotNil(t, main)
	require.Equal(t, mainURL, main.GetUrl(), "a saved row url is never signed")
	signed, ok := files.SignFileURLs(teamCtx, mainURL)
	require.True(t, ok, "the same url would be signed in any other field")
	require.NotEqual(t, mainURL, signed)

	read := func(t *testing.T) (*pb_admin.GetTechCardSavedAssemblySnapshotResponse, map[string]any) {
		t.Helper()
		profile := pb_admin.TechCardSavedAssemblySnapshotProfile_TECH_CARD_SAVED_ASSEMBLY_SNAPSHOT_PROFILE_COMPLETE_SOURCE_V1
		code, body := guardedRenameHTTP(t, gateway, http.MethodPost,
			fmt.Sprintf("/api/admin/tech-card/%d/patterns/%s:read-assembly-snapshot", cardID, x13MainRow),
			&pb_admin.GetTechCardSavedAssemblySnapshotRequest{
				TechCardId: int32(cardID), PatternLineKey: x13MainRow, ExpectedScopeKey: "main",
				ExpectedSavedRowRevision: main.GetSavedRowRevision(), ExpectedCardLockVersion: card.GetLockVersion(),
				ExpectedUrl: main.GetUrl(), ExpectedSizeBytes: main.GetSizeBytes(), ExpectedSha256: main.GetSha256(),
				Profile: &profile,
			})
		require.Equal(t, http.StatusOK, code, string(body))
		var response pb_admin.GetTechCardSavedAssemblySnapshotResponse
		require.NoError(t, protojson.Unmarshal(body, &response))
		var wire map[string]any
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		require.NoError(t, decoder.Decode(&wire))
		return &response, wire
	}
	snapshot, wire := read(t)
	require.Equal(t, mainURL, snapshot.GetRow().GetUrl())
	require.NotEmpty(t, snapshot.GetRow().GetDownloadUrl(), "the bytes still have a way in")
	verifyX13CompleteSourceSeal(t, snapshot, wire)

	// What the client sends back is built from the response: the V8 sidecar binds the row url.
	write := x13CrossRowSidecar(t, snapshot, sizeID, sizeToken)
	put := func(request *pb_admin.PutTechCardPatternSemanticSidecarRequest) (int, string) {
		code, body := guardedRenameHTTP(t, gateway, http.MethodPut,
			fmt.Sprintf("/api/admin/tech-card/%d/patterns/%s/semantic-sidecar", cardID, x13MainRow), request)
		return code, string(body)
	}

	// The failure this guards against: a sidecar carrying a signed copy of the url is refused, since
	// sidecar_bytes are bytes and nothing on the way in can cut a signature out of them.
	signedSnapshot := x13WithSignedRowURLs(snapshot, signed)
	code, body := put(x13CrossRowSidecar(t, signedSnapshot, sizeID, sizeToken))
	require.Equal(t, http.StatusBadRequest, code, body)

	code, body = put(write)
	require.Equal(t, http.StatusOK, code, body)

	after, afterWire := read(t)
	require.NotNil(t, after.GetSemanticSidecar())
	require.Equal(t, write.GetSidecarBytes(), after.GetSemanticSidecar().GetSidecarBytes())
	require.Equal(t, snapshot.GetCompleteSourceV1().GetSourceSha256(), after.GetCompleteSourceV1().GetSourceSha256())
	verifyX13CompleteSourceSeal(t, after, afterWire)
}

// newX13FileURLGateway is the loopback admin gateway with team #7 bound and the production file url
// interceptor right in front of the handlers.
func newX13FileURLGateway(t *testing.T, ctx context.Context, repository *store.MYSQLStore, files *bucket.Bucket, team tenant.Team) *httptest.Server {
	t.Helper()
	adminServer, err := admin.New(repository, files, nil, nil, nil, nil, nil, nil, nil, nil,
		entity.LabelAddress{}, "", "", jpk.Taxpayer{}, decimal.Zero)
	require.NoError(t, err)
	t.Cleanup(func() { adminServer.StopRevalidation(context.Background()) })
	srv := &Server{}
	srv.SetFileURLRewriter(files)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcServer := grpc.NewServer(grpc.ChainUnaryInterceptor(
		func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			ctx = tenant.With(ctx, team)
			ctx = authsrv.PutAdminAuthz(authsrv.PutAdminUsername(ctx, "x13-loopback-fixture"), authsrv.AdminAuthz{Super: true})
			return handler(ctx, req)
		},
		srv.fileURLUnary(),
	))
	pb_admin.RegisterAdminServiceServer(grpcServer, adminServer)
	done := make(chan error, 1)
	go func() { done <- grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		require.NoError(t, <-done)
	})
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	mux := newAdminServeMux()
	require.NoError(t, pb_admin.RegisterAdminServiceHandler(ctx, mux, connection))
	gateway := httptest.NewServer(mux)
	gateway.Client().Timeout = 10 * time.Second
	t.Cleanup(gateway.Close)
	return gateway
}

// seedX13TeamCard is the two-row, one-stitch card of the V8 store test with every file under team
// #7's private prefix, plus one technical picture.
func seedX13TeamCard(t *testing.T, ctx context.Context, repository *store.MYSQLStore) (cardID, sizeID int, sizeToken, mainURL string) {
	t.Helper()
	db, techCards := repository.DB(), repository.TechCards()
	var sizeName string
	require.NoError(t, db.QueryRowxContext(ctx, `SELECT id, name FROM size WHERE name <> '' ORDER BY id LIMIT 1`).Scan(&sizeID, &sizeName))
	sizeToken, _, _ = strings.Cut(strings.ToUpper(strings.TrimSpace(sizeName)), "_")
	require.NotEmpty(t, sizeToken)
	present := func(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }
	pattern := func(scope, key, digest string) entity.TechCardSizePattern {
		return entity.TechCardSizePattern{
			LineKey: key, FabricPurpose: present(scope), SizeId: sizeID, URL: x13TeamFileURL(scope + ".dxf"),
			Filename: present(scope + ".dxf"), SHA256: present(strings.Repeat(digest, 64)),
			SizeBytes: sql.NullInt64{Int64: 2048, Valid: true},
		}
	}
	picture := x13TeamFileURL("front.webp")
	inserted, err := db.ExecContext(ctx, `INSERT INTO media
		(full_size, full_size_width, full_size_height, thumbnail, thumbnail_width, thumbnail_height,
		compressed, compressed_width, compressed_height) VALUES (?, 200, 200, ?, 100, 100, ?, 100, 100)`,
		picture, picture, picture)
	require.NoError(t, err)
	mediaID, err := inserted.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := db.ExecContext(context.Background(), "DELETE FROM media WHERE id = ?", mediaID)
		require.NoError(t, err)
	})
	cardID, err = techCards.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "X13 private team card", StyleNumber: present("X13-SEAL"),
		Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm, SizeIds: []int{sizeID},
		BomItems: []entity.TechCardBomItem{
			{LineKey: "01JABCDEFGHJKMNPQRSTVWXYZ6", Name: "Shell", Section: entity.BomSectionFabric, Purpose: present("main")},
			{LineKey: "01JABCDEFGHJKMNPQRSTVWXYZ7", Name: "Pocket shell", Section: entity.BomSectionFabric, Purpose: present("pocketing")},
		},
		Patterns: []entity.TechCardSizePattern{pattern("main", x13MainRow, "a"), pattern("pocketing", x13PocketRow, "b")},
		Pieces: []entity.TechCardPiece{
			{LineKey: x13MainPiece, Name: "FRONT", PiecesPerGarment: 1, Grainline: "lengthwise"},
			{LineKey: x13PocketPiece, Name: "POCKET", PiecesPerGarment: 1, Grainline: "lengthwise"},
		},
		Media: []entity.TechCardMediaItem{{
			MediaId: int(mediaID), Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaFront,
		}},
		PieceDxfAliasesSet: true,
		PieceDxfAliases: []entity.TechCardPieceDxfAlias{
			{FabricPurpose: "main", BlockName: "FRONT", PieceLineKey: x13MainPiece},
			{FabricPurpose: "pocketing", BlockName: "POCKET", PieceLineKey: x13PocketPiece},
		},
		Operations: []entity.TechCardOperation{{
			OperationNumber: sql.NullInt32{Int32: 170, Valid: true},
			OperationType:   entity.OpTypeMachine, Zone: entity.ZoneOuter,
			Work: present("join_lockstitch"), MachineType: present("lockstitch"),
			InputKeys: []string{x13MainPiece, x13PocketPiece},
			AssemblyInputs: []entity.OperationInput{
				{Kind: entity.AssemblyInputPiece, Key: x13MainPiece},
				{Kind: entity.AssemblyInputPiece, Key: x13PocketPiece},
			},
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := db.ExecContext(context.Background(), "DELETE FROM tech_card_operation WHERE tech_card_id = ?", cardID)
		require.NoError(t, err)
		_, err = db.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", cardID)
		require.NoError(t, err)
	})
	anchor := func(piece string) []entity.TechCardSeamAnchor {
		return []entity.TechCardSeamAnchor{{
			PieceLineKey: piece, EdgeHint: piece + "#1+2", ContourSig: "abcdef0123456789",
			LenMm: 100, PerimShare: .25, Samples: []entity.TechCardSeamSample{{U: 0}, {U: .25}, {U: .5}, {U: .75}, {U: 1}},
		}}
	}
	stored, err := techCards.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	require.Len(t, stored.Operations, 1)
	number := int(stored.Operations[0].OperationNumber.Int32)
	poseA := anchor(x13MainPiece)
	poseA[0].Placement = &entity.TechCardSeamPlacement{XMm: 7, YMm: 4}
	_, err = techCards.ChangeTechCardSeams(ctx, entity.TechCardSeamsChange{
		TechCardId: cardID, ExpectedLockVersion: &stored.LockVersion, By: "x13-seal-test",
		Upsert: []entity.TechCardSeamInput{
			{SeamKey: x13Stitch, Status: entity.TechCardSeamConfirmed, Kind: entity.TechCardSeamKindEdge,
				Direction: entity.TechCardSeamReversed, Source: entity.TechCardSeamSourceManual,
				SideA: anchor(x13MainPiece), SideB: anchor(x13PocketPiece)},
			{SeamKey: x13Pose, Status: entity.TechCardSeamConfirmed, Kind: entity.TechCardSeamKindPose,
				Direction: entity.TechCardSeamDirectionUnknown, Source: entity.TechCardSeamSourceManual,
				SideA: poseA, SideB: anchor(x13PocketPiece)},
		},
		Link: []entity.TechCardSeamLink{{OperationNumber: number, SeamKey: x13Stitch}, {OperationNumber: number, SeamKey: x13Pose}},
	})
	require.NoError(t, err)
	return cardID, sizeID, sizeToken, x13TeamFileURL("main.dxf")
}

// verifyX13CompleteSourceSeal is the client's verifySourceDigest: the digest of the sealed bytes, and
// the sealed JSON equal to the projection of the very response the client received.
func verifyX13CompleteSourceSeal(t *testing.T, snapshot *pb_admin.GetTechCardSavedAssemblySnapshotResponse, wire map[string]any) {
	t.Helper()
	source := snapshot.GetCompleteSourceV1()
	require.NotNil(t, source)
	sum := sha256.Sum256(append([]byte("tech-card-saved-assembly-complete-source/1\x00"), source.GetCanonicalJson()...))
	require.Equal(t, source.GetSourceSha256(), hex.EncodeToString(sum[:]))
	wireSource := wire["completeSourceV1"].(map[string]any)
	rows := wireSource["rows"].([]any)
	for _, row := range rows {
		delete(row.(map[string]any), "downloadUrl")
	}
	projection := map[string]any{
		"serverAssemblyRevision": wire["serverAssemblyRevision"],
		"rows":                   rows,
		"operations":             wire["operations"],
		"ownedSeams":             wireSource["ownedSeams"],
		"aliases":                wireSource["aliases"],
		"garmentLabels":          wireSource["garmentLabels"],
	}
	if strips, _ := wireSource["cutStrips"].([]any); len(strips) > 0 {
		projection["cutStrips"] = strips
	}
	var sealed map[string]any
	decoder := json.NewDecoder(bytes.NewReader(source.GetCanonicalJson()))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&sealed))
	require.Equal(t, sealed, projection, "source fields differ from their verified seal")
}

// x13WithSignedRowURLs is the response as the interceptor used to send it: every row url signed.
func x13WithSignedRowURLs(snapshot *pb_admin.GetTechCardSavedAssemblySnapshotResponse, signedMain string) *pb_admin.GetTechCardSavedAssemblySnapshotResponse {
	out := protoCloneSnapshot(snapshot)
	bare := snapshot.GetRow().GetUrl()
	out.Row.Url = signedMain
	for _, row := range out.GetCompleteSourceV1().GetRows() {
		if row.GetUrl() == bare {
			row.Url = signedMain
		}
	}
	return out
}

func protoCloneSnapshot(m *pb_admin.GetTechCardSavedAssemblySnapshotResponse) *pb_admin.GetTechCardSavedAssemblySnapshotResponse {
	raw, err := protojson.Marshal(m)
	if err != nil {
		panic(err)
	}
	var out pb_admin.GetTechCardSavedAssemblySnapshotResponse
	if err := protojson.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return &out
}

// x13CrossRowSidecar is the store V8 fixture (nativeCrossRowReviewWriteFixtureV8) built from the
// wire response instead of the store entity: what a client derives from what it was sent.
func x13CrossRowSidecar(t *testing.T, snapshot *pb_admin.GetTechCardSavedAssemblySnapshotResponse, sizeID int, sizeToken string) *pb_admin.PutTechCardPatternSemanticSidecarRequest {
	t.Helper()
	source, row, revision := snapshot.GetCompleteSourceV1(), snapshot.GetRow(), snapshot.GetServerAssemblyRevision()
	require.Len(t, snapshot.GetOperations(), 1)
	operationNumber := snapshot.GetOperations()[0].GetOperationNumber()
	require.Positive(t, operationNumber)
	var sealed struct {
		OwnedSeams []struct {
			SeamKey string                            `json:"seamKey"`
			SideA   struct{ Parts []json.RawMessage } `json:"sideA"`
			SideB   struct{ Parts []json.RawMessage } `json:"sideB"`
		} `json:"ownedSeams"`
	}
	require.NoError(t, json.Unmarshal(source.GetCanonicalJson(), &sealed))
	require.Equal(t, x13Stitch, sealed.OwnedSeams[0].SeamKey)
	require.Equal(t, x13Pose, sealed.OwnedSeams[1].SeamKey)
	rowWire := func(r *pb_admin.TechCardSavedPatternRowIdentity) map[string]any {
		return map[string]any{
			"techCardId": r.GetTechCardId(), "scopeKey": r.GetScopeKey(), "patternLineKey": r.GetPatternLineKey(),
			"revision": r.GetSavedRowRevision(), "cardLockVersion": r.GetCardLockVersion(), "url": r.GetUrl(),
			"sizeBytes": r.GetSizeBytes(), "sha256": r.GetSha256(), "patternVersion": r.GetPatternVersion(),
		}
	}
	sides := [2][]json.RawMessage{sealed.OwnedSeams[0].SideA.Parts, sealed.OwnedSeams[0].SideB.Parts}
	require.Len(t, source.GetRows(), 2)
	members := make([]map[string]any, 2)
	for index, member := range source.GetRows() {
		require.Len(t, sides[index], 1)
		block, piece := "FRONT", x13MainPiece
		if index == 1 {
			block, piece = "POCKET", x13PocketPiece
		}
		sha, err := hex.DecodeString(member.GetSha256())
		require.NoError(t, err)
		fileID := "s/" + strconv.FormatInt(int64(member.GetTechCardId()), 36) + "/" + base64.RawURLEncoding.EncodeToString(sha)
		prefix := "dxf-vertex-v1/dxf-curve-v1/f/" + hex.EncodeToString([]byte(fileID)) + "/"
		members[index] = map[string]any{
			"row": rowWire(member), "sideIndex": index, "partOrdinal": 0, "pieceLineKey": piece,
			"blockName": block, "pieceId": block,
			"contourId":    "dxf-piece-v1/p/" + hex.EncodeToString([]byte(block)) + "/s/" + hex.EncodeToString([]byte(sizeToken)) + "/g/seam",
			"nativePathId": 101 + index, "sourceFileId": fileID, "direction": "forward",
			"orderedVertexIds": []string{prefix + "fixture-start", prefix + "fixture-end"},
			"capturedSavedAnchorSha256": entity.PatternSemanticSidecarPayloadSHA256(
				bytes.Join([][]byte{[]byte("tech-card-saved-assembly-native-anchor/1"), sides[index][0]}, []byte{0}),
			),
		}
	}
	payload, err := json.Marshal(map[string]any{
		"schemaVersion": "pattern-import-dxf-product-composite-sidecar-document/8",
		"mediaType":     entity.PatternProductCompositeSidecarV8MediaType, "encoding": "canonical-json-utf8",
		"profile":  "saved-full-assembly/native-cross-row-stitch/1",
		"bindings": map[string]any{"anchorRow": rowWire(row), "serverAssemblyRevision": revision, "completeSourceSha256": source.GetSourceSha256()},
		"writeCas": map[string]any{"expectedPreviousSidecarSha256": nil},
		"selection": map[string]any{
			"operationNumber": operationNumber, "operationOrdinal": 0, "ownedSeamKeys": []string{x13Stitch, x13Pose},
			"savedDirection": "reversed", "savedAnchoredSize": "", "sizeId": sizeID,
			"sizeToken": sizeToken, "sizeBasis": "operator-confirmed-native", "members": members,
		},
		"review": map[string]any{
			"sourceDecisionSha256": strings.Repeat("d", 64), "boundSourceRequestSha256": strings.Repeat("e", 64),
			"gridSha256": strings.Repeat("f", 64), "stationCount": 3,
		},
	})
	require.NoError(t, err)
	lock, savedRevision, prior, topology := row.GetCardLockVersion(), row.GetSavedRowRevision(), "", source.GetSourceSha256()
	return &pb_admin.PutTechCardPatternSemanticSidecarRequest{
		TechCardId: row.GetTechCardId(), PatternLineKey: row.GetPatternLineKey(),
		ExpectedCardLockVersion: &lock, ExpectedSavedRowRevision: &savedRevision,
		ExpectedDxfSha256: row.GetSha256(), ExpectedPatternVersion: row.GetPatternVersion(),
		ExpectedPreviousSidecarSha256: &prior, ExpectedServerAssemblyRevision: &revision,
		ExpectedAssemblyTopologySha256: &topology, SelectedOperationNumber: operationNumber,
		ExpectedPostSaveReceiptSha256: entity.PatternProductNativeCrossRowReviewReceiptV8(payload),
		MediaType:                     entity.PatternProductCompositeSidecarV8MediaType,
		SidecarSha256:                 entity.PatternSemanticSidecarPayloadSHA256(payload), SidecarBytes: payload,
	}
}
