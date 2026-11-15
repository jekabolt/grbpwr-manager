package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
)

type patternSemanticSidecarWireStub struct {
	pb_admin.UnimplementedAdminServiceServer
	putCalls      int
	getCalls      int
	snapshotCalls int
	lastPut       *pb_admin.PutTechCardPatternSemanticSidecarRequest
	lastGet       *pb_admin.GetTechCardPatternSemanticSidecarRequest
	lastSnapshot  *pb_admin.GetTechCardSavedAssemblySnapshotRequest
}

func (s *patternSemanticSidecarWireStub) GetTechCardSavedAssemblySnapshot(
	_ context.Context,
	req *pb_admin.GetTechCardSavedAssemblySnapshotRequest,
) (*pb_admin.GetTechCardSavedAssemblySnapshotResponse, error) {
	s.snapshotCalls++
	s.lastSnapshot = req
	return &pb_admin.GetTechCardSavedAssemblySnapshotResponse{
		Row: &pb_admin.TechCardSavedPatternRowIdentity{
			TechCardId: req.GetTechCardId(), ScopeKey: req.GetExpectedScopeKey(),
			PatternLineKey: req.GetPatternLineKey(), SavedRowRevision: req.GetExpectedSavedRowRevision(),
			CardLockVersion: req.GetExpectedCardLockVersion(), Url: req.GetExpectedUrl(),
			SizeBytes: req.GetExpectedSizeBytes(), Sha256: req.GetExpectedSha256(),
			DownloadUrl: "https://signed.example/pattern.dxf",
		},
		SelectedOperationNumber: req.GetSelectedOperationNumber(),
		ServerAssemblyRevision:  "asm:v1:0123456789abcdef0123456789abcdef:9",
		AssemblyTopologySha256:  strings.Repeat("d", 64),
		TopologyCanonicalJson:   []byte("{}"),
	}, nil
}

func (s *patternSemanticSidecarWireStub) PutTechCardPatternSemanticSidecar(
	_ context.Context,
	req *pb_admin.PutTechCardPatternSemanticSidecarRequest,
) (*pb_admin.PutTechCardPatternSemanticSidecarResponse, error) {
	s.putCalls++
	s.lastPut = req
	return &pb_admin.PutTechCardPatternSemanticSidecarResponse{
		SemanticSidecar: &pb_admin.TechCardPatternSemanticSidecar{
			TechCardId: req.GetTechCardId(), PatternLineKey: req.GetPatternLineKey(),
			CardLockVersion: req.GetExpectedCardLockVersion(), DxfSha256: req.GetExpectedDxfSha256(),
			PatternVersion: req.GetExpectedPatternVersion(), SidecarSha256: req.GetSidecarSha256(),
			MediaType: req.GetMediaType(), ByteLength: int32(len(req.GetSidecarBytes())),
			SidecarBytes: req.GetSidecarBytes(), PostSaveReceiptSha256: req.GetExpectedPostSaveReceiptSha256(),
			CreatedBy: "wire-test",
		},
	}, nil
}

func (s *patternSemanticSidecarWireStub) GetTechCardPatternSemanticSidecar(
	_ context.Context,
	req *pb_admin.GetTechCardPatternSemanticSidecarRequest,
) (*pb_admin.GetTechCardPatternSemanticSidecarResponse, error) {
	s.getCalls++
	s.lastGet = req
	return &pb_admin.GetTechCardPatternSemanticSidecarResponse{}, nil
}

func TestPatternSemanticSidecarProductionGatewayWire(t *testing.T) {
	stub := &patternSemanticSidecarWireStub{}
	mux := newAdminServeMux()
	require.NoError(t, pb_admin.RegisterAdminServiceHandlerServer(context.Background(), mux, stub))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	const lineKey = "01JABCDEFGHJKMNPQRSTVWXYZ0"
	body := `{"techCardId":44,"patternLineKey":"` + lineKey + `",` +
		`"expectedDxfSha256":"` + strings.Repeat("a", 64) + `","expectedPatternVersion":2,` +
		`"expectedPostSaveReceiptSha256":"` + strings.Repeat("b", 64) + `",` +
		`"mediaType":"application/vnd.grbpwr.pattern-dxf-semantics.v1+json",` +
		`"sidecarSha256":"` + strings.Repeat("c", 64) + `","sidecarBytes":"e30=",` +
		`"expectedCardLockVersion":0}`
	request, err := http.NewRequest(http.MethodPut,
		server.URL+"/api/admin/tech-card/44/patterns/"+lineKey+"/semantic-sidecar",
		strings.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	responseBody, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode, string(responseBody))
	require.Equal(t, 1, stub.putCalls)
	require.NotNil(t, stub.lastPut)
	require.EqualValues(t, 44, stub.lastPut.GetTechCardId())
	require.Equal(t, lineKey, stub.lastPut.GetPatternLineKey())
	require.NotNil(t, stub.lastPut.ExpectedCardLockVersion,
		"proto3 optional presence must survive JSON even when the exact lock token is zero")
	require.Zero(t, stub.lastPut.GetExpectedCardLockVersion())
	require.Equal(t, []byte("{}"), stub.lastPut.GetSidecarBytes())
	require.Contains(t, string(responseBody), `"sidecarBytes":"e30="`)
	require.Contains(t, string(responseBody), `"cardLockVersion":0`)

	getResponse, err := http.Get(server.URL + "/api/admin/tech-card/44/patterns/" + lineKey + "/semantic-sidecar")
	require.NoError(t, err)
	defer func() { _ = getResponse.Body.Close() }()
	require.Equal(t, http.StatusOK, getResponse.StatusCode)
	require.Equal(t, 1, stub.getCalls)
	require.EqualValues(t, 44, stub.lastGet.GetTechCardId())
	require.Equal(t, lineKey, stub.lastGet.GetPatternLineKey())

	snapshotBody := `{"techCardId":44,"patternLineKey":"` + lineKey + `",` +
		`"expectedScopeKey":"main","expectedSavedRowRevision":7,"expectedCardLockVersion":8,` +
		`"expectedUrl":"patterns/main.dxf","expectedSizeBytes":"1234",` +
		`"expectedSha256":"` + strings.Repeat("a", 64) + `","selectedOperationNumber":120}`
	snapshotRequest, err := http.NewRequest(http.MethodPost,
		server.URL+"/api/admin/tech-card/44/patterns/"+lineKey+":read-assembly-snapshot",
		strings.NewReader(snapshotBody))
	require.NoError(t, err)
	snapshotRequest.Header.Set("Content-Type", "application/json")
	snapshotResponse, err := http.DefaultClient.Do(snapshotRequest)
	require.NoError(t, err)
	defer func() { _ = snapshotResponse.Body.Close() }()
	snapshotResponseBody, err := io.ReadAll(snapshotResponse.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, snapshotResponse.StatusCode, string(snapshotResponseBody))
	require.Equal(t, 1, stub.snapshotCalls)
	require.EqualValues(t, 7, stub.lastSnapshot.GetExpectedSavedRowRevision())
	require.EqualValues(t, 1234, stub.lastSnapshot.GetExpectedSizeBytes())
	require.EqualValues(t, 120, stub.lastSnapshot.GetSelectedOperationNumber())
	require.Contains(t, string(snapshotResponseBody), `"topologyCanonicalJson":"e30="`)
	require.Contains(t, string(snapshotResponseBody), `"downloadUrl":"https://signed.example/pattern.dxf"`)

	badBody := strings.Replace(body, "expectedCardLockVersion", "expectedCardLockVerzion", 1)
	badRequest, err := http.NewRequest(http.MethodPut,
		server.URL+"/api/admin/tech-card/44/patterns/"+lineKey+"/semantic-sidecar",
		strings.NewReader(badBody))
	require.NoError(t, err)
	badRequest.Header.Set("Content-Type", "application/json")
	badResponse, err := http.DefaultClient.Do(badRequest)
	require.NoError(t, err)
	defer func() { _ = badResponse.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, badResponse.StatusCode)
	require.Equal(t, 1, stub.putCalls, "an unknown lock field must fail before the RPC")
}
