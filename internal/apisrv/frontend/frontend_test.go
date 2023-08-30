package frontend

// import (
// 	"context"
// 	"crypto/tls"
// 	"crypto/x509"
// 	"crypto/x509/pkix"
// 	"fmt"
// 	"net"
// 	"sort"
// 	"sync"
// 	"sync/atomic"
// 	"testing"
// 	"time"

// 	"github.com/go-chi/jwtauth/v5"
// 	"github.com/matryer/is"
// 	"go.mxc.org/airdrops/apisrv/airdrop"
// 	"go.mxc.org/airdrops/apisrv/auth"
// 	"go.mxc.org/airdrops/internal/jwt"
// 	pbairdrop "go.mxc.org/airdrops/proto/airdrop"
// 	pbbonus "go.mxc.org/airdrops/proto/bonus"
// 	"google.golang.org/grpc/credentials"
// 	"google.golang.org/grpc/metadata"
// 	"google.golang.org/grpc/peer"
// )

// type counter struct {
// 	c int64
// }

// func (c *counter) getNext() int64 {
// 	return atomic.AddInt64(&c.c, 1)
// }

// type testStore struct {
// 	airdrop.Store
// 	airdrops map[int64]*airdrop.Record
// 	m        sync.RWMutex
// 	c        *counter
// }

// func newTs() *testStore {
// 	return &testStore{
// 		airdrops: make(map[int64]*airdrop.Record),
// 		c: &counter{
// 			c: 0,
// 		},
// 		m: sync.RWMutex{},
// 	}
// }

// func (ts *testStore) BulkAdd(_ context.Context, airdrops []*airdrop.Record, sub string) error {
// 	ts.m.Lock()
// 	defer ts.m.Unlock()
// 	for _, airdrop := range airdrops {
// 		next := ts.c.getNext()
// 		airdrop.ID = next
// 		ts.airdrops[next] = airdrop
// 	}
// 	return nil
// }

// func (ts *testStore) List(_ context.Context) ([]*airdrop.Record, error) {
// 	ts.m.RLock()
// 	defer ts.m.RUnlock()
// 	var airdrops []*airdrop.Record
// 	for _, airdrop := range ts.airdrops {
// 		airdrops = append(airdrops, airdrop)
// 	}
// 	return airdrops, nil
// }

// func (ts *testStore) ListWhere(_ context.Context, failed, paid bool) ([]*airdrop.Record, error) {
// 	ts.m.RLock()
// 	defer ts.m.RUnlock()
// 	var airdrops []*airdrop.Record
// 	for _, airdrop := range ts.airdrops {
// 		if airdrop.Failed == failed && airdrop.Paid == paid {
// 			airdrops = append(airdrops, airdrop)
// 		}
// 	}
// 	return airdrops, nil
// }

// func (ts *testStore) ListBySupernodeWhere(ctx context.Context, supernode string, failed, paid bool) ([]*airdrop.Record, error) {
// 	ts.m.RLock()
// 	defer ts.m.RUnlock()
// 	var airdrops []*airdrop.Record
// 	for _, airdrop := range ts.airdrops {
// 		if airdrop.Supernode == supernode && airdrop.Failed == failed && airdrop.Paid == paid {
// 			airdrops = append(airdrops, airdrop)
// 		}
// 	}
// 	return airdrops, nil
// }

// func (ts *testStore) UpdateFailed(_ context.Context, id int64, errMsg string, _ string) error {
// 	ts.m.Lock()
// 	defer ts.m.Unlock()
// 	airdrop, ok := ts.airdrops[id]
// 	if !ok {
// 		return fmt.Errorf("airdrop not found")
// 	}
// 	airdrop.Failed = true
// 	airdrop.Error = errMsg
// 	ts.airdrops[id] = airdrop
// 	return nil
// }

// func (ts *testStore) UpdatePaid(ctx context.Context, id int64, _ string) error {
// 	ts.m.Lock()
// 	defer ts.m.Unlock()
// 	airdrop, ok := ts.airdrops[id]
// 	if !ok {
// 		return fmt.Errorf("airdrop not found")
// 	}
// 	airdrop.Paid = true
// 	ts.airdrops[id] = airdrop
// 	return nil
// }

// const (
// 	secret   = "secret"
// 	username = "username"
// )

// var (
// 	testDate = fmtDate(time.Now())
// )

// func fmtDate(date time.Time) string {
// 	return date.UTC().Format("2006-01-02")
// }

// // add authentication info into context
// func addPeer(ctx context.Context, peerName string) context.Context {
// 	p := peer.Peer{
// 		Addr: &net.IPAddr{
// 			IP: net.ParseIP("192.168.0.1"),
// 		},
// 		AuthInfo: credentials.TLSInfo{
// 			State: tls.ConnectionState{
// 				PeerCertificates: []*x509.Certificate{
// 					{
// 						Subject: pkix.Name{
// 							CommonName: peerName,
// 						},
// 					},
// 				},
// 			},
// 		},
// 	}
// 	return peer.NewContext(ctx, &p)
// }

// func TestBonus(t *testing.T) {

// 	ps := newTs()

// 	jwtInstance := jwtauth.New("HS256", []byte(secret), nil)

// 	http := airdrop.NewHTTP(ps, jwtInstance)

// 	token, err := jwt.NewToken(jwtInstance, time.Duration(time.Minute*100), username)
// 	is.NoErr(err)

// 	grpc := NewGRPC(ps)

// 	amount := fmt.Sprint(time.Now().Unix())
// 	// set up test cases
// 	tests := []struct {
// 		req  []*airdrop.Record
// 		want *pbairdrop.StatusResponse
// 	}{
// 		{
// 			req: []*airdrop.Record{
// 				{
// 					Email:     "test1",
// 					Supernode: "test.com",
// 					Amount:    amount,
// 					Currency:  "test",
// 					Purpose:   "purpose",
// 					Sub:       username,
// 				},
// 			},
// 			want: &pbairdrop.StatusResponse{
// 				Message: "OK",
// 			},
// 		},
// 		{
// 			req: []*airdrop.Record{
// 				{
// 					Email:     "test2",
// 					Supernode: "test.com",
// 					Amount:    amount,
// 					Currency:  "test",
// 					Purpose:   "purpose",
// 					Sub:       username,
// 				},
// 			},
// 			want: &pbairdrop.StatusResponse{
// 				Message: "OK",
// 			},
// 		},
// 	}

// 	for _, tt := range tests {
// 		req := &pbairdrop.AirdropListSubmit{
// 			Airdrops: airdrop.AirdropsSubmitToProtoMsg(tt.req),
// 		}
// 		header := metadata.New(map[string]string{auth.AuthMetadataKey: fmt.Sprintf("Bearer %s", token)})
// 		// this is the critical step that includes your headers
// 		ctx := metadata.NewIncomingContext(context.Background(), header)

// 		resp, err := http.BulkSubmit(ctx, req)
// 		is.NoErr(err)
// 		is.Equal(resp.Message, tt.want.Message)
// 	}
// 	respFailed, err := http.ListFailed(context.Background(), nil)
// 	is.NoErr(err)
// 	is.True(len(respFailed.Airdrops) == 0)

// 	resp, err := http.List(context.Background(), nil)
// 	is.NoErr(err)

// 	is.True(resp.Airdrops != nil)
// 	is.True(len(resp.Airdrops) == 2)

// 	respPending, err := http.ListPending(context.Background(), nil)
// 	is.NoErr(err)

// 	is.True(respPending.Airdrops != nil)

// 	sort.Slice(respPending.Airdrops, func(i, j int) bool {
// 		return respPending.GetAirdrops()[i].GetId() < respPending.GetAirdrops()[j].GetId()
// 	})

// 	sort.Slice(resp.Airdrops, func(i, j int) bool {
// 		return respPending.GetAirdrops()[i].GetId() < respPending.GetAirdrops()[j].GetId()
// 	})

// 	is.Equal(respPending.Airdrops, resp.Airdrops)

// 	ctx := addPeer(context.Background(), "test.com")
// 	resp, err = grpc.ListBySupernode(ctx, &pbbonus.ListBySupernodeRequest{
// 		Failed: true,
// 		Paid:   false,
// 	})
// 	is.NoErr(err)

// 	is.True(len(respPending.Airdrops) == 2)

// 	toBePaid := respPending.Airdrops[0]

// 	_, err = grpc.UpdatePaid(ctx, &pbbonus.UpdatePaidRequest{
// 		Id: toBePaid.Id,
// 	})
// 	is.NoErr(err)

// 	resp, err = http.ListPending(context.Background(), nil)
// 	is.NoErr(err)
// 	is.True(len(resp.Airdrops) == 1)

// 	toBeFailed := resp.Airdrops[0]

// 	testErr := "err"
// 	_, err = grpc.UpdateError(ctx, &pbbonus.UpdateErrorRequest{
// 		Id:    toBeFailed.Id,
// 		Error: testErr,
// 	})
// 	is.NoErr(err)

// 	respFailed, err = http.ListFailed(context.Background(), nil)
// 	is.NoErr(err)
// 	is.True(len(respFailed.Airdrops) == 1)
// 	is.True(respFailed.Airdrops[0].Error == testErr)

// 	respPending, err = http.ListPending(context.Background(), nil)
// 	is.NoErr(err)

// 	is.True(len(respPending.Airdrops) == 0)

// 	resp, err = http.List(context.Background(), nil)
// 	is.NoErr(err)
// 	is.True(len(resp.Airdrops) == 2)

// }
