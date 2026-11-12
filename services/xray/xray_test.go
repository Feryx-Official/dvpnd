// SPDX-License-Identifier: Apache-2.0

package xray

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	proxymancommand "github.com/xtls/xray-core/app/proxyman/command"
	statscommand "github.com/xtls/xray-core/app/stats/command"
	"github.com/xtls/xray-core/proxy/vless"
	"google.golang.org/grpc"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
	"github.com/Feryx-Official/dvpnd/v9/services/common/daemontest"
	xraytypes "github.com/Feryx-Official/dvpnd/v9/services/xray/types"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// fakeAPI stands in for xray's gRPC control API: it records the users added
// and removed on the VLESS inbound and answers traffic queries from a table,
// which keeps a removed user's counters, as xray does.
type fakeAPI struct {
	proxymancommand.UnimplementedHandlerServiceServer
	statscommand.UnimplementedStatsServiceServer

	users   map[string]*vless.Account
	removed []string
	stats   map[string]int64
}

func (f *fakeAPI) AlterInbound(_ context.Context, req *proxymancommand.AlterInboundRequest) (*proxymancommand.AlterInboundResponse, error) {
	if req.GetTag() != xraytypes.InboundTag {
		return nil, grpc.Errorf(2, "unknown inbound %q", req.GetTag())
	}

	op, err := req.GetOperation().GetInstance()
	if err != nil {
		return nil, err
	}

	switch op := op.(type) {
	case *proxymancommand.AddUserOperation:
		acc, err := op.GetUser().GetAccount().GetInstance()
		if err != nil {
			return nil, err
		}
		f.users[op.GetUser().GetEmail()] = acc.(*vless.Account)
	case *proxymancommand.RemoveUserOperation:
		if _, ok := f.users[op.GetEmail()]; !ok {
			return nil, grpc.Errorf(5, "user %q not found", op.GetEmail())
		}
		delete(f.users, op.GetEmail())
		f.removed = append(f.removed, op.GetEmail())
	default:
		return nil, grpc.Errorf(2, "unexpected operation %T", op)
	}

	return &proxymancommand.AlterInboundResponse{}, nil
}

func (f *fakeAPI) QueryStats(_ context.Context, req *statscommand.QueryStatsRequest) (*statscommand.QueryStatsResponse, error) {
	res := &statscommand.QueryStatsResponse{}
	for name, value := range f.stats {
		if strings.Contains(name, req.GetPattern()) {
			res.Stat = append(res.Stat, &statscommand.Stat{Name: name, Value: value})
			if req.GetReset_() {
				f.stats[name] = 0
			}
		}
	}

	return res, nil
}

func startFakeAPI(t *testing.T) (*fakeAPI, uint16) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	api := &fakeAPI{users: map[string]*vless.Account{}, stats: map[string]int64{}}
	srv := grpc.NewServer()
	proxymancommand.RegisterHandlerServiceServer(srv, api)
	statscommand.RegisterStatsServiceServer(srv, api)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	return api, uint16(lis.Addr().(*net.TCPAddr).Port)
}

// home writes a node home with the TLS files and an xray.toml with the given
// security, and points the service's temp config at the same directory.
func home(t *testing.T, security string) (string, *xraytypes.Config) {
	t.Helper()

	dir := t.TempDir()
	certPEM, keyPEM, err := common.SelfSignedCertificate("dvpnd", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tls.crt"), certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tls.key"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := xraytypes.NewConfig().WithDefaultValues()
	cfg.VLESS.ListenPort = 8443
	cfg.VLESS.Security = security
	if err := cfg.SaveToPath(filepath.Join(dir, xraytypes.ConfigFileName)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", dir)

	return dir, cfg
}

func stubBinary(t *testing.T) {
	t.Helper()
	stubScript(t, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile :; do sleep 0.1; done\n")
}

// stubScript puts script where the xray binary is looked for.
func stubScript(t *testing.T, script string) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "xray")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	saved := binaryName
	binaryName = path
	t.Cleanup(func() { binaryName = saved })
}

func TestInitRendersTLSConfig(t *testing.T) {
	stubBinary(t)
	dir, cfg := home(t, xraytypes.SecurityTLS)

	s := NewXRay()
	if err := s.Init(dir); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "xray_config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("rendered config is not JSON: %v\n%s", err, raw)
	}

	inbounds := doc["inbounds"].([]interface{})
	api := inbounds[0].(map[string]interface{})
	in := inbounds[1].(map[string]interface{})
	if api["port"].(float64) != float64(cfg.API.Port) || api["listen"] != "127.0.0.1" {
		t.Fatalf("api inbound: %v", api)
	}
	stream := in["streamSettings"].(map[string]interface{})
	if in["port"].(float64) != 8443 || in["protocol"] != "vless" || stream["security"] != "tls" {
		t.Fatalf("vless inbound: %v", in)
	}
	if _, ok := stream["realitySettings"]; ok {
		t.Fatal("tls config must not carry realitySettings")
	}
	certs := stream["tlsSettings"].(map[string]interface{})["certificates"].([]interface{})
	if certs[0].(map[string]interface{})["certificateFile"] != filepath.Join(dir, "tls.crt") {
		t.Fatalf("certificate path: %v", certs)
	}

	if s.ListenPort() != 8443 || s.info[2] != proxyVLESS || s.info[3] != types.TransportProtocolTCP ||
		s.info[4] != types.TransportSecurityTLS || len(s.TLSPin()) != 64 {
		t.Fatalf("info %x pin %q", s.info, s.TLSPin())
	}
}

func TestInitRendersRealityConfig(t *testing.T) {
	stubBinary(t)
	dir, cfg := home(t, xraytypes.SecurityReality)

	s := NewXRay()
	if err := s.Init(dir); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, "xray_config.json"))
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("rendered config is not JSON: %v\n%s", err, raw)
	}
	stream := doc["inbounds"].([]interface{})[1].(map[string]interface{})["streamSettings"].(map[string]interface{})
	reality := stream["realitySettings"].(map[string]interface{})
	if stream["security"] != "reality" || reality["dest"] != "www.apple.com:443" ||
		reality["privateKey"] != cfg.Reality.PrivateKey ||
		reality["shortIds"].([]interface{})[0] != cfg.Reality.ShortID {
		t.Fatalf("reality settings: %v", reality)
	}
	if _, ok := stream["tlsSettings"]; ok {
		t.Fatal("reality config must not carry tlsSettings")
	}
	if s.info[4] != types.TransportSecurityReality || s.TLSPin() != "" {
		t.Fatalf("info %x pin %q", s.info, s.TLSPin())
	}
}

// Rules: [RT-6].
func TestInitRequiresBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	saved := binaryName
	binaryName = "xray"
	t.Cleanup(func() { binaryName = saved })

	err := NewXRay().Init(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), `"xray" binary is not on PATH`) {
		t.Fatalf("Init without the binary: %v", err)
	}
}

// TestInitRefusesCleartextVLESS: VLESS has no cipher of its own, so with a
// security other than TLS or REALITY the proxy hop would be cleartext. The
// node refuses to start instead, before it renders a config for xray.
//
// Rules: [PV-9].
func TestInitRefusesCleartextVLESS(t *testing.T) {
	stubBinary(t)
	dir, _ := home(t, "none")

	err := NewXRay().Init(dir)
	if err == nil || !strings.Contains(err.Error(), "security must be") {
		t.Fatalf("Init with security none: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "xray_config.json")); !os.IsNotExist(err) {
		t.Fatalf("a config for xray was rendered: %v", err)
	}
}

// Rules: [SL-1], [CT-10].
func TestPeersOverTheAPI(t *testing.T) {
	api, port := startFakeAPI(t)

	s := NewXRay()
	s.config = xraytypes.NewConfig().WithDefaultValues()
	s.config.API.Port = port
	t.Cleanup(func() {
		if s.conn != nil {
			_ = s.conn.Close()
		}
	})

	data, err := s.ParsePeerRequest([]byte(`{"uuid":"01020304-0506-0708-090a-0b0c0d0e0f10"}`))
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(data)

	if _, err := s.AddPeer(data); err != nil {
		t.Fatal(err)
	}
	acc, ok := api.users[key]
	if !ok || acc.Id != "01020304-0506-0708-090a-0b0c0d0e0f10" || acc.Flow != flowVision || acc.Encryption != "none" {
		t.Fatalf("user added: %v %+v", ok, acc)
	}
	if !s.HasPeer(data) || s.PeerCount() != 1 {
		t.Fatal("peer not registered")
	}

	api.stats["user>>>"+key+">>>traffic>>>uplink"] = 10
	api.stats["user>>>"+key+">>>traffic>>>downlink"] = 20
	api.stats["user>>>other>>>traffic>>>uplink"] = 99
	peers, err := s.Peers()
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].Key != key || peers[0].Upload != 10 || peers[0].Download != 20 {
		t.Fatalf("peers: %+v", peers)
	}

	if err := s.RemovePeer(data); err != nil {
		t.Fatal(err)
	}
	if s.HasPeer(data) || len(api.removed) != 1 || api.removed[0] != key {
		t.Fatalf("peer not removed: %v", api.removed)
	}
	// Removing again is not an error: xray answers "not found".
	if err := s.RemovePeer(data); err != nil {
		t.Fatalf("second removal: %v", err)
	}

	if _, err := s.AddPeer([]byte{1, 2, 3}); err == nil {
		t.Fatal("short peer data accepted")
	}

	s.config.VLESS.Flow = false
	if _, err := s.AddPeer(data); err != nil {
		t.Fatal(err)
	}
	if api.users[key].Flow != "" {
		t.Fatalf("flow off must add a user without flow: %+v", api.users[key])
	}
}

// TestAPeerAddedAgainStartsFromZero: xray keeps a user's counters once the
// user is removed (seen with the real binary), so a later session with the
// client's UUID would start with what the earlier one moved. The node bills
// on the counters from the moment it adds a peer.
//
// Rules: [SL-2].
func TestAPeerAddedAgainStartsFromZero(t *testing.T) {
	api, port := startFakeAPI(t)

	s := NewXRay()
	s.config = xraytypes.NewConfig().WithDefaultValues()
	s.config.API.Port = port
	t.Cleanup(func() {
		if s.conn != nil {
			_ = s.conn.Close()
		}
	})

	data, err := s.ParsePeerRequest([]byte(`{"uuid":"01020304-0506-0708-090a-0b0c0d0e0f10"}`))
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(data)
	if _, err := s.AddPeer(data); err != nil {
		t.Fatal(err)
	}
	api.stats["user>>>"+key+">>>traffic>>>uplink"] = 10
	api.stats["user>>>"+key+">>>traffic>>>downlink"] = 20
	api.stats["user>>>other>>>traffic>>>uplink"] = 99
	if err := s.RemovePeer(data); err != nil {
		t.Fatal(err)
	}

	if _, err := s.AddPeer(data); err != nil {
		t.Fatal(err)
	}
	peers, err := s.Peers()
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].Upload != 0 || peers[0].Download != 0 {
		t.Fatalf("want the peer added again to start from zero, got %+v", peers)
	}
	if api.stats["user>>>other>>>traffic>>>uplink"] != 99 {
		t.Fatal("another user's counter was reset")
	}
}

// Rules: [RT-3].
func TestStartStop(t *testing.T) {
	stubBinary(t)
	dir, _ := home(t, xraytypes.SecurityTLS)

	s := NewXRay()
	if err := s.Init(dir); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if exited, _ := s.process.Exited(); !exited {
		t.Fatal("child was not reaped")
	}
}

// TestDoneWhenTheProxyDies: a proxy that dies closes Done, on which the start
// command stops the node, since it would serve nothing while active.
//
// Rules: [RT-15].
func TestDoneWhenTheProxyDies(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	stubScript(t, daemontest.Script(pidFile))
	dir, _ := home(t, xraytypes.SecurityTLS)

	s := NewXRay()
	if err := s.Init(dir); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	daemontest.Crash(t, pidFile, s.Done())
}

// Rules: [CT-3], [CT-4].
func TestHandshakePayload(t *testing.T) {
	stubBinary(t)

	dir, cfg := home(t, xraytypes.SecurityTLS)
	s := NewXRay()
	if err := s.Init(dir); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(mustPayload(t, s))
	want := `{"metadata":[{"port":8443,"proxy_protocol":1,"transport_protocol":1,"transport_security":2,"tls_pin":"` + s.TLSPin() + `","flow":2}]}`
	if string(out) != want {
		t.Fatalf("tls payload:\n got %s\nwant %s", out, want)
	}
	if pub, _ := json.Marshal(s.PublicMetadata()); string(pub) != `[{"port":"","proxy_protocol":1,"transport_protocol":1,"transport_security":2,"flow":2,"method":"","key":"","tls_pin":"","reality_server_name":"","reality_short_id":"","reality_public_key":"","reality_fingerprint":""}]` {
		t.Fatalf("public listing must carry codes and flow only: %s", pub)
	}

	dir, cfg = home(t, xraytypes.SecurityReality)
	s = NewXRay()
	if err := s.Init(dir); err != nil {
		t.Fatal(err)
	}
	out, _ = json.Marshal(mustPayload(t, s))
	want = `{"metadata":[{"port":8443,"proxy_protocol":1,"transport_protocol":1,"transport_security":3,"flow":2,` +
		`"reality_server_name":"www.apple.com","reality_short_id":"` + cfg.Reality.ShortID + `",` +
		`"reality_public_key":"` + cfg.Reality.PublicKey + `","reality_fingerprint":"chrome"}]}`
	if string(out) != want {
		t.Fatalf("reality payload:\n got %s\nwant %s", out, want)
	}
	if pub, _ := json.Marshal(s.PublicMetadata()); !strings.Contains(string(pub), `"transport_security":3`) || strings.Contains(string(pub), cfg.Reality.PublicKey) {
		t.Fatalf("public listing must not carry reality keys: %s", pub)
	}
	if s.Name() != "xray" || s.Type() != 4 {
		t.Fatalf("identity: %s/%d", s.Name(), s.Type())
	}
}

func mustPayload(t *testing.T, s *XRay) interface{} {
	t.Helper()
	payload, err := s.HandshakePayload(nil)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// TestLogLevelFollowsNodeLogLevel: xray's warnings carry client addresses and
// destinations, so they are kept only when the node runs at debug itself.
//
// Rules: [PV-2].
func TestLogLevelFollowsNodeLogLevel(t *testing.T) {
	stubBinary(t)
	t.Cleanup(func() { common.SetVerbose(false) })

	for _, tc := range []struct {
		verbose bool
		want    string
	}{
		{verbose: false, want: "error"},
		{verbose: true, want: "warning"},
	} {
		common.SetVerbose(tc.verbose)
		dir, _ := home(t, xraytypes.SecurityTLS)
		if err := NewXRay().Init(dir); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "xray_config.json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Log struct {
				Access   string `json:"access"`
				LogLevel string `json:"loglevel"`
			} `json:"log"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Log.LogLevel != tc.want || doc.Log.Access != "none" {
			t.Errorf("verbose %v: log section %+v, want loglevel %q and no access log", tc.verbose, doc.Log, tc.want)
		}
	}
}

// routingRules is the rendered config's routing rules, keyed by what they
// match; it fails the test unless every rule but the API's ends in the
// blackhole outbound.
func routingRules(t *testing.T, dir string) (map[string]interface{}, map[string]map[string]interface{}) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(dir, "xray_config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("rendered config is not JSON: %v\n%s", err, raw)
	}

	blackhole := false
	for _, o := range doc["outbounds"].([]interface{}) {
		o := o.(map[string]interface{})
		if o["tag"] == "blocked" && o["protocol"] == "blackhole" {
			blackhole = true
		}
	}
	if !blackhole {
		t.Fatalf("no blackhole outbound tagged blocked: %v", doc["outbounds"])
	}
	if first := doc["outbounds"].([]interface{})[0].(map[string]interface{}); first["protocol"] != "freedom" {
		t.Fatalf("the default (first) outbound must be freedom: %v", first)
	}

	routing := doc["routing"].(map[string]interface{})
	rules := map[string]map[string]interface{}{}
	for _, r := range routing["rules"].([]interface{}) {
		r := r.(map[string]interface{})
		switch {
		case r["inboundTag"] != nil:
			rules["api"] = r
			continue
		case r["domain"] != nil:
			rules["domain"] = r
		case r["ip"] != nil:
			rules["ip"] = r
		case r["port"] != nil:
			rules["port"] = r
		default:
			t.Fatalf("unexpected rule %v", r)
		}
		if r["outboundTag"] != "blocked" {
			t.Fatalf("rule %v does not block", r)
		}
	}

	return routing, rules
}

// TestInitRendersEgressPolicy: a client cannot reach the blocked networks,
// loopback by name (the control API first of all), or port 25 unless the
// operator allows it.
//
// Rules: [EG-1], [EG-4], [EG-5].
func TestInitRendersEgressPolicy(t *testing.T) {
	stubBinary(t)
	t.Cleanup(func() { common.SetEgress(common.Egress{}) })

	dir, _ := home(t, xraytypes.SecurityReality)
	if err := NewXRay().Init(dir); err != nil {
		t.Fatal(err)
	}
	routing, rules := routingRules(t, dir)

	if routing["domainStrategy"] != "IPIfNonMatch" {
		t.Fatalf("domainStrategy %v: a name that resolves to loopback would pass", routing["domainStrategy"])
	}
	var ips []string
	for _, ip := range rules["ip"]["ip"].([]interface{}) {
		ips = append(ips, ip.(string))
	}
	if strings.Join(ips, " ") != strings.Join(common.BlockedNetworks(), " ") {
		t.Fatalf("blocked networks: %v", ips)
	}
	if d := rules["domain"]["domain"].([]interface{}); len(d) != 1 || d[0] != "domain:localhost" {
		t.Fatalf("domain rule: %v", d)
	}
	if p := rules["port"]; p == nil || p["port"] != "25" || p["network"] != "tcp" {
		t.Fatalf("port 25 rule: %v", p)
	}
	if api := rules["api"]; api["outboundTag"] != "api" {
		t.Fatalf("api rule: %v", api)
	}

	common.SetEgress(common.Egress{AllowSMTP: true})
	if err := NewXRay().Init(dir); err != nil {
		t.Fatal(err)
	}
	if _, rules = routingRules(t, dir); rules["port"] != nil || rules["ip"] == nil {
		t.Fatalf("allow_smtp: rules %v", rules)
	}
}

// TestConfigStringsReadBackAsThemselves: every string the node writes into
// xray's configuration is JSON-encoded, so a quote or a backslash in the
// REALITY server name or in the runtime directory's path neither breaks the
// file nor adds a key.
//
// Rules: [RT-22].
func TestConfigStringsReadBackAsThemselves(t *testing.T) {
	stubBinary(t)
	odd := `a"b\c`

	dir, cfg := home(t, xraytypes.SecurityReality)
	cfg.Reality.ServerName = odd + ".example"
	if err := cfg.SaveToPath(filepath.Join(dir, xraytypes.ConfigFileName)); err != nil {
		t.Fatal(err)
	}
	if err := NewXRay().Init(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "xray_config.json"))
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("a quote in server_name broke the config: %v\n%s", err, raw)
	}
	reality := doc["inbounds"].([]interface{})[1].(map[string]interface{})["streamSettings"].(map[string]interface{})["realitySettings"].(map[string]interface{})
	if reality["serverNames"].([]interface{})[0] != cfg.Reality.ServerName || reality["dest"] != cfg.Reality.ServerName+":443" {
		t.Fatalf("reality settings: %v", reality)
	}

	dir, _ = home(t, xraytypes.SecurityTLS)
	run := filepath.Join(dir, odd)
	if err := os.Mkdir(run, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", run)
	if err := NewXRay().Init(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(run, "xray_config.json"))
	doc = nil
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("a quote in the runtime path broke the config: %v\n%s", err, raw)
	}
	certs := doc["inbounds"].([]interface{})[1].(map[string]interface{})["streamSettings"].(map[string]interface{})["tlsSettings"].(map[string]interface{})["certificates"].([]interface{})
	cert := certs[0].(map[string]interface{})
	if cert["certificateFile"] != filepath.Join(run, "tls.crt") || cert["keyFile"] != filepath.Join(run, "tls.key") {
		t.Fatalf("certificate paths: %v", cert)
	}
}
