package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
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
	getSidecar    *pb_admin.TechCardPatternSemanticSidecar
}

func (s *patternSemanticSidecarWireStub) GetTechCardSavedAssemblySnapshot(
	_ context.Context,
	req *pb_admin.GetTechCardSavedAssemblySnapshotRequest,
) (*pb_admin.GetTechCardSavedAssemblySnapshotResponse, error) {
	s.snapshotCalls++
	s.lastSnapshot = req
	out := &pb_admin.GetTechCardSavedAssemblySnapshotResponse{
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
	}
	if req.GetProfile() == pb_admin.TechCardSavedAssemblySnapshotProfile_TECH_CARD_SAVED_ASSEMBLY_SNAPSHOT_PROFILE_COMPLETE_SOURCE_V1 {
		out.AssemblyTopologySha256 = ""
		out.TopologyCanonicalJson = nil
		out.CompleteSourceV1 = &pb_admin.TechCardSavedAssemblyCompleteSourceV1{
			Rows: []*pb_admin.TechCardSavedPatternRowIdentity{out.Row},
			OwnedSeams: []*pb_common.TechCardSeam{{
				Kind: pb_common.TechCardSeamKind_TECH_CARD_SEAM_KIND_POSE,
			}},
			GarmentLabels: []*pb_common.TechCardGarmentLabel{{LineKey: "01M75ACEBB74BMT91RJ7WSTTJY"}},
			SourceSha256:  strings.Repeat("a", 64), CanonicalJson: []byte("{}"),
		}
	}
	return out, nil
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
	return &pb_admin.GetTechCardPatternSemanticSidecarResponse{SemanticSidecar: s.getSidecar}, nil
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
	require.NotContains(t, string(snapshotResponseBody), "completeSourceV1",
		"selected V1 must not gain an unpopulated extra field")

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

func TestGetTechCardSavedAssemblySnapshotCompleteSourceGatewayWire(t *testing.T) {
	stub := &patternSemanticSidecarWireStub{}
	mux := newAdminServeMux()
	require.NoError(t, pb_admin.RegisterAdminServiceHandlerServer(context.Background(), mux, stub))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	const line = "01JABCDEFGHJKMNPQRSTVWXYZ0"
	sha := strings.Repeat("a", 64)
	body := `{"techCardId":44,"patternLineKey":"` + line + `",` +
		`"expectedScopeKey":"main","expectedSavedRowRevision":7,"expectedCardLockVersion":8,` +
		`"expectedUrl":"patterns/main.dxf","expectedSizeBytes":"1234","expectedSha256":"` + sha + `",` +
		`"profile":"TECH_CARD_SAVED_ASSEMBLY_SNAPSHOT_PROFILE_COMPLETE_SOURCE_V1",` +
		`"expectedCompleteSourceSha256":"` + sha + `"}`
	response, err := http.Post(
		server.URL+"/api/admin/tech-card/44/patterns/"+line+":read-assembly-snapshot",
		"application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	res, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode, string(res))
	require.EqualValues(t, 0, stub.lastSnapshot.GetSelectedOperationNumber())
	require.Equal(t, pb_admin.TechCardSavedAssemblySnapshotProfile_TECH_CARD_SAVED_ASSEMBLY_SNAPSHOT_PROFILE_COMPLETE_SOURCE_V1, stub.lastSnapshot.GetProfile())
	require.Equal(t, sha, stub.lastSnapshot.GetExpectedCompleteSourceSha256())
	require.Contains(t, string(res), `"completeSourceV1":{`)
	require.Contains(t, string(res), `"kind":"TECH_CARD_SEAM_KIND_POSE"`)
	require.Contains(t, string(res), `"anchoredSize":""`)
	require.Contains(t, string(res), `"garmentLabels":[`)
	require.Contains(t, string(res), `"lineKey":"01M75ACEBB74BMT91RJ7WSTTJY"`)
	require.Contains(t, string(res), `"canonicalJson":"e30="`)
}

func TestPatternSemanticSidecarCompositeV5GatewayPreservesAssemblyCASAndEcho(t *testing.T) {
	stub := &patternSemanticSidecarWireStub{}
	mux := newAdminServeMux()
	require.NoError(t, pb_admin.RegisterAdminServiceHandlerServer(context.Background(), mux, stub))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	const lineKey = "01JABCDEFGHJKMNPQRSTVWXYZ0"
	previousSHA := strings.Repeat("c", 64)
	topologySHA := strings.Repeat("d", 64)
	body := `{"techCardId":44,"patternLineKey":"` + lineKey + `",` +
		`"expectedDxfSha256":"` + strings.Repeat("a", 64) + `","expectedPatternVersion":2,` +
		`"expectedPostSaveReceiptSha256":"` + strings.Repeat("b", 64) + `",` +
		`"mediaType":"application/vnd.grbpwr.pattern-dxf-product-composite.v5+json",` +
		`"sidecarSha256":"` + strings.Repeat("e", 64) + `","sidecarBytes":"eyJ2Ijo1fQ==",` +
		`"expectedCardLockVersion":8,"expectedSavedRowRevision":7,` +
		`"expectedPreviousSidecarSha256":"` + previousSHA + `",` +
		`"expectedServerAssemblyRevision":"asm:v1:0123456789abcdef0123456789abcdef:9",` +
		`"expectedAssemblyTopologySha256":"` + topologySHA + `","selectedOperationNumber":120}`
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
	require.Equal(t, "application/vnd.grbpwr.pattern-dxf-product-composite.v5+json", stub.lastPut.GetMediaType())
	require.Equal(t, []byte(`{"v":5}`), stub.lastPut.GetSidecarBytes())
	require.NotNil(t, stub.lastPut.ExpectedCardLockVersion)
	require.EqualValues(t, 8, stub.lastPut.GetExpectedCardLockVersion())
	require.NotNil(t, stub.lastPut.ExpectedSavedRowRevision)
	require.EqualValues(t, 7, stub.lastPut.GetExpectedSavedRowRevision())
	require.NotNil(t, stub.lastPut.ExpectedPreviousSidecarSha256)
	require.Equal(t, previousSHA, stub.lastPut.GetExpectedPreviousSidecarSha256())
	require.NotNil(t, stub.lastPut.ExpectedServerAssemblyRevision)
	require.Equal(t, "asm:v1:0123456789abcdef0123456789abcdef:9", stub.lastPut.GetExpectedServerAssemblyRevision())
	require.NotNil(t, stub.lastPut.ExpectedAssemblyTopologySha256)
	require.Equal(t, topologySHA, stub.lastPut.GetExpectedAssemblyTopologySha256())
	require.EqualValues(t, 120, stub.lastPut.GetSelectedOperationNumber())
	require.Contains(t, string(responseBody), `"mediaType":"application/vnd.grbpwr.pattern-dxf-product-composite.v5+json"`)
	require.Contains(t, string(responseBody), `"sidecarBytes":"eyJ2Ijo1fQ=="`)

	stub.getSidecar = &pb_admin.TechCardPatternSemanticSidecar{
		TechCardId: 44, PatternLineKey: lineKey,
		MediaType:    "application/vnd.grbpwr.pattern-dxf-product-composite.v5+json",
		SidecarBytes: []byte(`{"v":5}`), SidecarSha256: strings.Repeat("e", 64),
		ByteLength: 7,
	}
	getResponse, err := http.Get(server.URL + "/api/admin/tech-card/44/patterns/" + lineKey + "/semantic-sidecar")
	require.NoError(t, err)
	defer func() { _ = getResponse.Body.Close() }()
	getBody, err := io.ReadAll(getResponse.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getResponse.StatusCode, string(getBody))
	require.Equal(t, 1, stub.getCalls)
	require.Contains(t, string(getBody), `"mediaType":"application/vnd.grbpwr.pattern-dxf-product-composite.v5+json"`)
	require.Contains(t, string(getBody), `"sidecarBytes":"eyJ2Ijo1fQ=="`)
}

func TestPatternSemanticSidecarCompositeV4GatewayPreservesAssemblyCASAndEcho(t *testing.T) {
	stub := &patternSemanticSidecarWireStub{}
	mux := newAdminServeMux()
	require.NoError(t, pb_admin.RegisterAdminServiceHandlerServer(context.Background(), mux, stub))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	const lineKey = "01JABCDEFGHJKMNPQRSTVWXYZ0"
	previousSHA := strings.Repeat("c", 64)
	topologySHA := strings.Repeat("d", 64)
	body := `{"techCardId":44,"patternLineKey":"` + lineKey + `",` +
		`"expectedDxfSha256":"` + strings.Repeat("a", 64) + `","expectedPatternVersion":2,` +
		`"expectedPostSaveReceiptSha256":"` + strings.Repeat("b", 64) + `",` +
		`"mediaType":"application/vnd.grbpwr.pattern-dxf-product-composite.v4+json",` +
		`"sidecarSha256":"` + strings.Repeat("e", 64) + `","sidecarBytes":"eyJ2Ijo0fQ==",` +
		`"expectedCardLockVersion":8,"expectedSavedRowRevision":7,` +
		`"expectedPreviousSidecarSha256":"` + previousSHA + `",` +
		`"expectedServerAssemblyRevision":"asm:v1:0123456789abcdef0123456789abcdef:9",` +
		`"expectedAssemblyTopologySha256":"` + topologySHA + `","selectedOperationNumber":120}`
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
	require.Equal(t, "application/vnd.grbpwr.pattern-dxf-product-composite.v4+json", stub.lastPut.GetMediaType())
	require.Equal(t, []byte(`{"v":4}`), stub.lastPut.GetSidecarBytes())
	require.EqualValues(t, 8, stub.lastPut.GetExpectedCardLockVersion())
	require.EqualValues(t, 7, stub.lastPut.GetExpectedSavedRowRevision())
	require.Equal(t, previousSHA, stub.lastPut.GetExpectedPreviousSidecarSha256())
	require.Equal(t, "asm:v1:0123456789abcdef0123456789abcdef:9", stub.lastPut.GetExpectedServerAssemblyRevision())
	require.Equal(t, topologySHA, stub.lastPut.GetExpectedAssemblyTopologySha256())
	require.EqualValues(t, 120, stub.lastPut.GetSelectedOperationNumber())
	require.Contains(t, string(responseBody), `"mediaType":"application/vnd.grbpwr.pattern-dxf-product-composite.v4+json"`)
	require.Contains(t, string(responseBody), `"sidecarBytes":"eyJ2Ijo0fQ=="`)

	stub.getSidecar = &pb_admin.TechCardPatternSemanticSidecar{
		TechCardId: 44, PatternLineKey: lineKey,
		MediaType:    "application/vnd.grbpwr.pattern-dxf-product-composite.v4+json",
		SidecarBytes: []byte(`{"v":4}`), SidecarSha256: strings.Repeat("e", 64),
		ByteLength: 7,
	}
	getResponse, err := http.Get(server.URL + "/api/admin/tech-card/44/patterns/" + lineKey + "/semantic-sidecar")
	require.NoError(t, err)
	defer func() { _ = getResponse.Body.Close() }()
	getBody, err := io.ReadAll(getResponse.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getResponse.StatusCode, string(getBody))
	require.Equal(t, 1, stub.getCalls)
	require.Contains(t, string(getBody), `"mediaType":"application/vnd.grbpwr.pattern-dxf-product-composite.v4+json"`)
	require.Contains(t, string(getBody), `"sidecarBytes":"eyJ2Ijo0fQ=="`)
}
