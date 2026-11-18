package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/jpk"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/testdbguard"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	renameMainBOM       = "01JABCDEFGHJKMNPQRSTVWXYZ0"
	renamePocketBOM     = "01JABCDEFGHJKMNPQRSTVWXYZ1"
	renameMainPattern   = "01JABCDEFGHJKMNPQRSTVWXYZ2"
	renamePocketPattern = "01JABCDEFGHJKMNPQRSTVWXYZ3"
	renameFrontPiece    = "01JABCDEFGHJKMNPQRSTVWXYZ4"
	renamePocketPiece   = "01JABCDEFGHJKMNPQRSTVWXYZ5"
	renameOldLabel      = "Front"
	renameNewLabel      = "Передняя половинка"
)

// TestGuardedPieceRenameGatewayMySQL uses an explicitly selected, disposable loopback database.
// The generated JSON gateway forwards to the actual gRPC admin service and MySQL write owner.
func TestGuardedPieceRenameGatewayMySQL(t *testing.T) {
	dsn := os.Getenv("AC10_GATEWAY_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set AC10_GATEWAY_TEST_MYSQL_DSN to a disposable loopback MySQL database")
	}
	require.NoError(t, testdbguard.CheckMySQLDSN(dsn))
	config, err := mysql.ParseDSN(dsn)
	require.NoError(t, err)
	host, _, err := net.SplitHostPort(config.Addr)
	require.NoError(t, err)
	require.True(t, net.ParseIP(host).IsLoopback(), "use a numeric loopback host")
	dto.SetManagedPatternHosts("ac10-gateway.invalid")
	t.Cleanup(func() { dto.SetManagedPatternHosts() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	repository, err := store.NewForTest(ctx, store.Config{
		DSN: dsn, Automigrate: true, MaxOpenConnections: 10, MaxIdleConnections: 5,
	})
	require.NoError(t, err)
	t.Cleanup(repository.Close)
	gateway := newGuardedRenameGateway(t, ctx, repository)
	seed := func(t *testing.T) *pb_common.TechCard {
		t.Helper()
		return seedGuardedRenameGatewayCard(t, ctx, repository, gateway)
	}
	read := func(t *testing.T, card *pb_common.TechCard) *pb_common.TechCard {
		t.Helper()
		return readGuardedRenameGatewayCard(t, gateway, card.GetId())
	}

	t.Run("exact refusal and label-only write", func(t *testing.T) {
		before := seed(t)
		for _, changed := range []struct {
			name       string
			mutate     func(*pb_admin.UpdateTechCardRequest)
			httpStatus int
		}{
			{"lock", func(r *pb_admin.UpdateTechCardRequest) { r.ExpectedLockVersion++ }, http.StatusConflict},
			{"piece label", func(r *pb_admin.UpdateTechCardRequest) { r.GuardedPieceRename.ExpectedName = "Already changed" }, http.StatusBadRequest},
			{"piece line", func(r *pb_admin.UpdateTechCardRequest) { r.GuardedPieceRename.PieceLineKey = renamePocketPiece }, http.StatusBadRequest},
			{"scope", func(r *pb_admin.UpdateTechCardRequest) { r.GuardedPieceRename.ScopeKey = "pocketing" }, http.StatusBadRequest},
			{"row key", func(r *pb_admin.UpdateTechCardRequest) { r.GuardedPieceRename.PatternLineKey = renamePocketPattern }, http.StatusBadRequest},
			{"saved row revision", func(r *pb_admin.UpdateTechCardRequest) { r.GuardedPieceRename.ExpectedSavedRowRevision++ }, http.StatusBadRequest},
			{"DXF SHA", func(r *pb_admin.UpdateTechCardRequest) {
				r.GuardedPieceRename.ExpectedDxfSha256 = strings.Repeat("e", 64)
			}, http.StatusBadRequest},
		} {
			t.Run(changed.name, func(t *testing.T) {
				request := guardedRenameRequest(before)
				changed.mutate(request)
				code, body := guardedRenameHTTP(t, gateway, http.MethodPost, "/api/admin/tech-card/update", request)
				require.Equal(t, changed.httpStatus, code, string(body))
				require.True(t, proto.Equal(before, read(t, before)), "refusal changed the saved card")
			})
		}

		request := guardedRenameRequest(before)
		code, body := guardedRenameHTTP(t, gateway, http.MethodPost, "/api/admin/tech-card/update", request)
		require.Equal(t, http.StatusOK, code, string(body))
		var response pb_admin.UpdateTechCardResponse
		require.NoError(t, protojson.Unmarshal(body, &response))
		require.Equal(t, before.GetLockVersion()+1, response.GetLockVersion())
		after := read(t, before)
		require.Equal(t, response.GetLockVersion(), after.GetLockVersion())
		assertGuardedRenameRoundTrip(t, before, after)
	})

	t.Run("prospective pattern drift rolls back", func(t *testing.T) {
		card := seed(t)
		// A historical stored display name can be padded. The ordinary DTO trims that
		// field, so this exercises a post-write refusal after both pattern rows were touched.
		const storedName = "  Pocket cut pattern  "
		_, err := repository.DB().ExecContext(ctx,
			"UPDATE tech_card_size_pattern SET name = ? WHERE tech_card_id = ? AND line_key = ?",
			storedName, card.GetId(), renamePocketPattern)
		require.NoError(t, err)
		before := read(t, card)
		require.Equal(t, storedName, guardedRenamePattern(before, renamePocketPattern).GetName())
		type writeClocks struct {
			AssemblyRevision uint64 `db:"assembly_revision"`
			JournalEntries   int    `db:"journal_entries"`
		}
		readClocks := func() writeClocks {
			t.Helper()
			var clocks writeClocks
			require.NoError(t, repository.DB().GetContext(ctx, &clocks, `SELECT revision AS assembly_revision,
				(SELECT COUNT(*) FROM tech_card_revision WHERE tech_card_id = ?) AS journal_entries
				FROM tech_card_assembly_clock WHERE tech_card_id = ?`, card.GetId(), card.GetId()))
			return clocks
		}
		initialClocks := readClocks()
		code, body := guardedRenameHTTP(t, gateway, http.MethodPost, "/api/admin/tech-card/update", guardedRenameRequest(before))
		after := read(t, before)
		require.Equal(t, http.StatusBadRequest, code, "%s; other pattern name is now %q", body,
			guardedRenamePattern(after, renamePocketPattern).GetName())
		require.Contains(t, string(body), entity.ErrTechCardPieceRenameProtected.Error())
		require.True(t, proto.Equal(before, after), "failed rewrite leaked a partial update")
		require.Equal(t, initialClocks, readClocks())
	})

	t.Run("other pattern V1 wins", func(t *testing.T) {
		before := seed(t)
		path, attach := guardedRenameV1Request(before, renamePocketPattern)
		code, body := guardedRenameHTTP(t, gateway, http.MethodPut, path, attach)
		require.Equal(t, http.StatusOK, code, string(body))
		require.True(t, proto.Equal(before, read(t, before)), "V1 changed the card lock or pattern binding")
		code, body = guardedRenameHTTP(t, gateway, http.MethodPost, "/api/admin/tech-card/update", guardedRenameRequest(before))
		require.Equal(t, http.StatusBadRequest, code, string(body))
		require.Contains(t, string(body), entity.ErrTechCardPieceRenameProtected.Error())
		require.True(t, proto.Equal(before, read(t, before)), "guarded refusal changed the card")
		code, body = guardedRenameHTTP(t, gateway, http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, code, string(body))
		assertGuardedRenameV1Read(t, attach, body)
	})

	t.Run("rename wins", func(t *testing.T) {
		before := seed(t)
		path, attach := guardedRenameV1Request(before, renameMainPattern)
		code, body := guardedRenameHTTP(t, gateway, http.MethodPost, "/api/admin/tech-card/update", guardedRenameRequest(before))
		require.Equal(t, http.StatusOK, code, string(body))
		code, body = guardedRenameHTTP(t, gateway, http.MethodPut, path, attach)
		require.Equal(t, http.StatusBadRequest, code, string(body))
		require.Contains(t, string(body), "pattern_binding_moved")
		code, body = guardedRenameHTTP(t, gateway, http.MethodGet, path, nil)
		require.Equal(t, http.StatusNotFound, code, string(body))
		assertGuardedRenameRoundTrip(t, before, read(t, before))
	})

	t.Run("simultaneous saves", func(t *testing.T) {
		before := seed(t)
		sidecarPath, attach := guardedRenameV1Request(before, renamePocketPattern)
		rename := guardedRenameRequest(before)
		type result struct {
			code int
			body []byte
			err  error
		}
		begin := make(chan struct{})
		put, post := make(chan result, 1), make(chan result, 1)
		go func() {
			<-begin
			code, body, err := doGuardedRenameHTTP(gateway, http.MethodPut, sidecarPath, attach)
			put <- result{code, body, err}
		}()
		go func() {
			<-begin
			code, body, err := doGuardedRenameHTTP(gateway, http.MethodPost, "/api/admin/tech-card/update", rename)
			post <- result{code, body, err}
		}()
		close(begin)
		v1Result, renameResult := <-put, <-post
		require.NoError(t, v1Result.err)
		require.NoError(t, renameResult.err)
		t.Logf("concurrent outcomes: V1=%d rename=%d", v1Result.code, renameResult.code)
		switch {
		case v1Result.code == http.StatusOK:
			require.Equal(t, http.StatusBadRequest, renameResult.code, string(renameResult.body))
			require.Contains(t, string(renameResult.body), entity.ErrTechCardPieceRenameProtected.Error())
			require.True(t, proto.Equal(before, read(t, before)))
			code, body := guardedRenameHTTP(t, gateway, http.MethodGet, sidecarPath, nil)
			require.Equal(t, http.StatusOK, code, string(body))
			assertGuardedRenameV1Read(t, attach, body)
		case renameResult.code == http.StatusOK:
			require.Equal(t, http.StatusBadRequest, v1Result.code, string(v1Result.body))
			require.Contains(t, string(v1Result.body), "pattern_binding_moved")
			assertGuardedRenameRoundTrip(t, before, read(t, before))
			code, body := guardedRenameHTTP(t, gateway, http.MethodGet, sidecarPath, nil)
			require.Equal(t, http.StatusNotFound, code, string(body))
		default:
			t.Fatalf("no winner: V1 %d %s; rename %d %s", v1Result.code, v1Result.body, renameResult.code, renameResult.body)
		}
	})
}

func newGuardedRenameGateway(t *testing.T, ctx context.Context, repository *store.MYSQLStore) *httptest.Server {
	t.Helper()
	adminServer, err := admin.New(repository, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		entity.LabelAddress{}, "", "", jpk.Taxpayer{}, decimal.Zero)
	require.NoError(t, err)
	t.Cleanup(func() { adminServer.StopRevalidation(context.Background()) })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		// This private loopback server carries a synthetic actor and never loads an auth service.
		ctx = authsrv.PutAdminAuthz(authsrv.PutAdminUsername(ctx, "ac10-loopback-fixture"), authsrv.AdminAuthz{Super: true})
		return handler(ctx, req)
	}))
	pb_admin.RegisterAdminServiceServer(grpcServer, adminServer)
	grpcDone := make(chan error, 1)
	go func() { grpcDone <- grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		require.NoError(t, <-grpcDone)
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

func seedGuardedRenameGatewayCard(t *testing.T, ctx context.Context, repository *store.MYSQLStore, gateway *httptest.Server) *pb_common.TechCard {
	t.Helper()
	present := func(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }
	pattern := func(scope, bom, line, name, digest string) entity.TechCardSizePattern {
		return entity.TechCardSizePattern{
			LineKey: line, BomLineKey: present(bom), FabricPurpose: present(scope),
			URL:      "https://ac10-gateway.invalid/tech-card-patterns/" + name + ".dxf",
			Filename: present(name + ".dxf"), Name: present(name + " cut pattern"),
			SHA256: present(strings.Repeat(digest, 64)), SizeBytes: sql.NullInt64{Int64: 2001, Valid: true},
		}
	}
	media, err := repository.DB().ExecContext(ctx, `INSERT INTO media
		(full_size, full_size_width, full_size_height, thumbnail, thumbnail_width, thumbnail_height,
		compressed, compressed_width, compressed_height)
		VALUES ('https://ac10-gateway.invalid/front.webp', 200, 200, 'thumb.webp', 100, 100, 'small.webp', 100, 100)`)
	require.NoError(t, err)
	mediaID, err := media.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := repository.DB().ExecContext(context.Background(), "DELETE FROM media WHERE id = ?", mediaID)
		require.NoError(t, err)
	})
	cardID, err := repository.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "AC10 gateway front and pocket", StyleNumber: present("AC10-GATEWAY"), Stage: entity.TechCardStageProto,
		ApprovalState: entity.TechCardApprovalDraft, MeasurementUnit: entity.TechCardUnitMm,
		BomItems: []entity.TechCardBomItem{
			{LineKey: renameMainBOM, Section: entity.BomSectionFabric, Name: "Shell", Purpose: present("main")},
			{LineKey: renamePocketBOM, Section: entity.BomSectionFabric, Name: "Pocketing", Purpose: present("pocketing")},
		},
		Pieces: []entity.TechCardPiece{
			{LineKey: renameFrontPiece, Name: renameOldLabel, PiecesPerGarment: 2, Grainline: "lengthwise",
				CalloutNumber: sql.NullInt32{Int32: 1, Valid: true}},
			{LineKey: renamePocketPiece, Name: "Pocket", PiecesPerGarment: 1, Grainline: "lengthwise"},
		},
		Media: []entity.TechCardMediaItem{{
			MediaId: int(mediaID), Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaFront,
		}},
		Callouts: []entity.TechCardCallout{{
			Number: 1, Part: present(renameOldLabel), Parts: []string{renameOldLabel, "trim detail"},
			Description: present("Preserve this note"), MediaId: sql.NullInt32{Int32: int32(mediaID), Valid: true},
		}},
		Patterns: []entity.TechCardSizePattern{
			pattern("main", renameMainBOM, renameMainPattern, "main", "a"),
			pattern("pocketing", renamePocketBOM, renamePocketPattern, "pocketing", "b"),
		},
		PieceDxfAliasesSet: true,
		PieceDxfAliases: []entity.TechCardPieceDxfAlias{
			{BomLineKey: renameMainBOM, FabricPurpose: "main", BlockName: "FRONT_M", PieceLineKey: renameFrontPiece},
			{BomLineKey: renameMainBOM, FabricPurpose: "main", BlockName: "FRONT_L", PieceLineKey: renameFrontPiece},
			{BomLineKey: renamePocketBOM, FabricPurpose: "pocketing", BlockName: "POCKET_M", PieceLineKey: renamePocketPiece},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := repository.DB().ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", cardID)
		require.NoError(t, err)
	})
	card := readGuardedRenameGatewayCard(t, gateway, int32(cardID))
	// The guarded command is available after the ordinary saved-card update has issued a
	// positive lock (as it does when a wizard applies a DXF to an existing card).
	code, body := guardedRenameHTTP(t, gateway, http.MethodPost, "/api/admin/tech-card/update", &pb_admin.UpdateTechCardRequest{
		Id: card.GetId(), ExpectedLockVersion: card.GetLockVersion(), TechCard: card.GetTechCard(),
	})
	require.Equal(t, http.StatusOK, code, string(body))
	card = readGuardedRenameGatewayCard(t, gateway, int32(cardID))
	require.Positive(t, card.GetLockVersion())
	require.Positive(t, guardedRenamePattern(card, renameMainPattern).GetSavedRowRevision())
	require.Empty(t, card.GetTechCard().GetOperations())
	require.Len(t, card.GetTechCard().GetPatterns(), 2)
	require.Len(t, card.GetTechCard().GetPieceDxfAliases().GetItems(), 3)
	require.Equal(t, []string{renameOldLabel, "trim detail"}, card.GetTechCard().GetCallouts()[0].GetParts())
	return card
}

func guardedRenameRequest(card *pb_common.TechCard) *pb_admin.UpdateTechCardRequest {
	pattern := guardedRenamePattern(card, renameMainPattern)
	return &pb_admin.UpdateTechCardRequest{
		Id: card.GetId(), ExpectedLockVersion: card.GetLockVersion(),
		GuardedPieceRename: &pb_admin.GuardedZeroOperationPieceRename{
			PieceLineKey: renameFrontPiece, ExpectedName: renameOldLabel,
			NextName: renameNewLabel, ScopeKey: "main", PatternLineKey: pattern.GetLineKey(),
			ExpectedSavedRowRevision: pattern.GetSavedRowRevision(), ExpectedDxfSha256: pattern.GetSha256(),
		},
	}
}

func guardedRenamePattern(card *pb_common.TechCard, line string) *pb_common.TechCardSizePattern {
	for _, candidate := range card.GetTechCard().GetPatterns() {
		if candidate.GetLineKey() == line {
			return candidate
		}
	}
	return nil
}

func readGuardedRenameGatewayCard(t *testing.T, gateway *httptest.Server, cardID int32) *pb_common.TechCard {
	t.Helper()
	code, body := guardedRenameHTTP(t, gateway, http.MethodGet, fmt.Sprintf("/api/admin/tech-card/%d", cardID), nil)
	require.Equal(t, http.StatusOK, code, string(body))
	var response pb_admin.GetTechCardResponse
	require.NoError(t, protojson.Unmarshal(body, &response))
	require.NotNil(t, response.GetTechCard())
	return response.GetTechCard()
}

func assertGuardedRenameRoundTrip(t *testing.T, before, after *pb_common.TechCard) {
	t.Helper()
	require.Equal(t, before.GetLockVersion()+1, after.GetLockVersion())
	want := proto.CloneOf(before.GetTechCard())
	for _, piece := range want.GetPieces() {
		if piece.GetLineKey() == renameFrontPiece {
			piece.Name = renameNewLabel
		}
	}
	for _, pattern := range want.GetPatterns() {
		pattern.SavedRowRevision++
	}
	callout := want.GetCallouts()[0]
	callout.Part = renameNewLabel
	callout.Parts[0] = renameNewLabel
	got := after.GetTechCard()
	require.True(t, proto.Equal(want, got), "document differs outside the label, its callout mirror, or expected pattern revision\nwant: %s\ngot: %s", want, got)
}

func guardedRenameV1Request(card *pb_common.TechCard, line string) (string, *pb_admin.PutTechCardPatternSemanticSidecarRequest) {
	pattern := guardedRenamePattern(card, line)
	media := entity.PatternProductSemanticSidecarMediaType
	lock, revision := card.GetLockVersion(), pattern.GetSavedRowRevision()
	receipt := strings.Repeat("d", 64)
	payload := []byte(fmt.Sprintf(
		`{"schemaVersion":"pattern-import-dxf-product-semantic-sidecar-document/1","mediaType":%q,"encoding":"canonical-json-utf8","product":{"exportSha256":%q},"persistence":{"receiptSealSha256":%q,"card":{"techCardId":%d,"observedLockVersion":%d},"pattern":{"lineKey":%q,"version":%d,"sha256":%q}}}`,
		media, pattern.GetSha256(), receipt, card.GetId(), lock, line, pattern.GetVersion(), pattern.GetSha256(),
	))
	path := fmt.Sprintf("/api/admin/tech-card/%d/patterns/%s/semantic-sidecar", card.GetId(), line)
	return path, &pb_admin.PutTechCardPatternSemanticSidecarRequest{
		ExpectedCardLockVersion: &lock, ExpectedSavedRowRevision: &revision,
		ExpectedDxfSha256: pattern.GetSha256(), ExpectedPatternVersion: pattern.GetVersion(),
		ExpectedPostSaveReceiptSha256: receipt, MediaType: media,
		SidecarSha256: entity.PatternSemanticSidecarPayloadSHA256(payload), SidecarBytes: payload,
	}
}

func assertGuardedRenameV1Read(t *testing.T, request *pb_admin.PutTechCardPatternSemanticSidecarRequest, body []byte) {
	t.Helper()
	var response pb_admin.GetTechCardPatternSemanticSidecarResponse
	require.NoError(t, protojson.Unmarshal(body, &response))
	require.Equal(t, request.GetSidecarBytes(), response.GetSemanticSidecar().GetSidecarBytes())
	require.Equal(t, request.GetSidecarSha256(), response.GetSemanticSidecar().GetSidecarSha256())
}

func guardedRenameHTTP(t *testing.T, gateway *httptest.Server, method, path string, request proto.Message) (int, []byte) {
	t.Helper()
	code, response, err := doGuardedRenameHTTP(gateway, method, path, request)
	require.NoError(t, err)
	return code, response
}

func doGuardedRenameHTTP(gateway *httptest.Server, method, path string, request proto.Message) (int, []byte, error) {
	var payload []byte
	if request != nil {
		var err error
		payload, err = protojson.Marshal(request)
		if err != nil {
			return 0, nil, err
		}
	}
	r, err := http.NewRequest(method, gateway.URL+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := gateway.Client().Do(r)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return response.StatusCode, body, err
}
