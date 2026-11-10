// SPDX-License-Identifier: Apache-2.0

package v2ray

import (
	"context"
	"encoding/base64"
	"net"
	"sync"
	"testing"
	"time"

	proxymancommand "github.com/v2fly/v2ray-core/v5/app/proxyman/command"
	statscommand "github.com/v2fly/v2ray-core/v5/app/stats/command"
	"github.com/v2fly/v2ray-core/v5/common/serial"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	v2raytypes "github.com/Feryx-Official/dvpnd/v9/services/v2ray/types"
)

// fakeAPI stands in for v2ray's gRPC control API: it records the users on
// the VMess inbound and answers traffic queries from a table. stall makes
// every call hang, as a proxy that stopped answering would.
type fakeAPI struct {
	proxymancommand.UnimplementedHandlerServiceServer
	statscommand.UnimplementedStatsServiceServer

	mu    sync.Mutex
	users map[string]bool
	stats map[string]int64
	stall bool
}

// wait blocks a stalled call until its deadline and fails it then; the
// server's copy of the deadline can expire just before the client's, so a
// stalled call must not go on to succeed.
func (f *fakeAPI) wait(ctx context.Context) error {
	f.mu.Lock()
	stall := f.stall
	f.mu.Unlock()
	if stall {
		<-ctx.Done()
		return ctx.Err()
	}

	return nil
}

func (f *fakeAPI) AlterInbound(ctx context.Context, req *proxymancommand.AlterInboundRequest) (*proxymancommand.AlterInboundResponse, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	if req.GetTag() != "vmess" {
		return nil, status.Errorf(codes.Unknown, "unknown inbound %q", req.GetTag())
	}
	op, err := serial.GetInstanceOf(req.GetOperation())
	if err != nil {
		return nil, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	switch op := op.(type) {
	case *proxymancommand.AddUserOperation:
		f.users[op.GetUser().GetEmail()] = true
	case *proxymancommand.RemoveUserOperation:
		if !f.users[op.GetEmail()] {
			return nil, status.Errorf(codes.Unknown, "user %q not found", op.GetEmail())
		}
		delete(f.users, op.GetEmail())
	}

	return &proxymancommand.AlterInboundResponse{}, nil
}

func (f *fakeAPI) GetStats(ctx context.Context, req *statscommand.GetStatsRequest) (*statscommand.GetStatsResponse, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.stats[req.GetName()]
	if !ok {
		return nil, status.Errorf(codes.Unknown, "%s not found", req.GetName())
	}

	return &statscommand.GetStatsResponse{Stat: &statscommand.Stat{Name: req.GetName(), Value: v}}, nil
}

func startFakeAPI(t *testing.T) (*fakeAPI, *V2Ray) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{users: map[string]bool{}, stats: map[string]int64{}}
	srv := grpc.NewServer()
	proxymancommand.RegisterHandlerServiceServer(srv, api)
	statscommand.RegisterStatsServiceServer(srv, api)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	s := NewV2Ray()
	s.config = v2raytypes.NewConfig().WithDefaultValues()
	s.config.API.Port = uint16(lis.Addr().(*net.TCPAddr).Port)
	t.Cleanup(func() {
		if s.conn != nil {
			_ = s.conn.Close()
		}
	})

	return api, s
}

// Rules: [SL-1].
func TestPeersOverTheAPI(t *testing.T) {
	api, s := startFakeAPI(t)

	data := append([]byte{0x01}, []byte("0123456789abcdef")...)
	key := base64.StdEncoding.EncodeToString(data)
	if _, err := s.AddPeer(data); err != nil {
		t.Fatal(err)
	}
	if !api.users[key] || !s.HasPeer(data) || s.PeerCount() != 1 {
		t.Fatalf("peer not added: users %v", api.users)
	}

	// Counters the proxy has not created yet read as zero.
	peers, err := s.Peers()
	if err != nil || len(peers) != 1 || peers[0].Key != key || peers[0].Upload != 0 {
		t.Fatalf("peers %+v, %v", peers, err)
	}
	api.mu.Lock()
	api.stats["user>>>"+key+">>>traffic>>>uplink"] = 10
	api.stats["user>>>"+key+">>>traffic>>>downlink"] = 20
	api.mu.Unlock()
	if peers, err = s.Peers(); err != nil || peers[0].Upload != 10 || peers[0].Download != 20 {
		t.Fatalf("peers %+v, %v", peers, err)
	}

	if err := s.RemovePeer(data); err != nil {
		t.Fatal(err)
	}
	// A user the proxy no longer has is not an error.
	if err := s.RemovePeer(data); err != nil {
		t.Fatal(err)
	}
	if api.users[key] || s.HasPeer(data) {
		t.Fatal("peer not removed")
	}
}

// TestAPICallsAreBounded: a proxy that stops answering fails the call after
// rpcTimeout instead of holding the handshake or the job forever.
//
// Rules: [RT-5].
func TestAPICallsAreBounded(t *testing.T) {
	saved := rpcTimeout
	rpcTimeout = 200 * time.Millisecond
	t.Cleanup(func() { rpcTimeout = saved })

	api, s := startFakeAPI(t)
	api.mu.Lock()
	api.stall = true
	api.mu.Unlock()

	start := time.Now()
	_, err := s.AddPeer(append([]byte{0x01}, []byte("0123456789abcdef")...))
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("AddPeer against a stalled proxy: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("AddPeer took %s", elapsed)
	}
}

// TestAPICallWaitsForTheProxy: a call made while the proxy is still starting
// (a handshake right after the node starts) waits for its API, within
// rpcTimeout, instead of failing at once; with no proxy at all it fails at
// rpcTimeout.
//
// Rules: [RT-5].
func TestAPICallWaitsForTheProxy(t *testing.T) {
	saved := rpcTimeout
	rpcTimeout = 3 * time.Second
	t.Cleanup(func() { rpcTimeout = saved })

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()

	s := NewV2Ray()
	s.config = v2raytypes.NewConfig().WithDefaultValues()
	s.config.API.Port = uint16(port)
	t.Cleanup(func() {
		if s.conn != nil {
			_ = s.conn.Close()
		}
	})

	api := &fakeAPI{users: map[string]bool{}, stats: map[string]int64{}}
	srv := grpc.NewServer()
	proxymancommand.RegisterHandlerServiceServer(srv, api)
	t.Cleanup(srv.Stop)
	go func() {
		time.Sleep(300 * time.Millisecond)
		lis, err := net.Listen("tcp", lis.Addr().String())
		if err != nil {
			return
		}
		_ = srv.Serve(lis)
	}()

	if _, err := s.AddPeer(append([]byte{0x01}, []byte("0123456789abcdef")...)); err != nil {
		t.Fatalf("AddPeer while the proxy starts: %v", err)
	}

	rpcTimeout = 300 * time.Millisecond
	idle := NewV2Ray()
	idle.config = v2raytypes.NewConfig().WithDefaultValues()
	idle.config.API.Port = uint16(port + 1)
	t.Cleanup(func() {
		if idle.conn != nil {
			_ = idle.conn.Close()
		}
	})
	start := time.Now()
	if _, err := idle.AddPeer(append([]byte{0x01}, []byte("0123456789abcdef")...)); err == nil {
		t.Fatal("AddPeer with no proxy succeeded")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("AddPeer with no proxy took %s", elapsed)
	}
}
