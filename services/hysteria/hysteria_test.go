// SPDX-License-Identifier: Apache-2.0

package hysteria

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
	"github.com/Feryx-Official/dvpnd/v9/services/common/daemontest"
	hysteriatypes "github.com/Feryx-Official/dvpnd/v9/services/hysteria/types"
)

// fakeStats stands in for the server's statistics API: it serves a traffic
// table once (clear=1 semantics) and records kicks; failKick fails them, and
// breakClear cuts the reply to a clearing read short, after the clear.
type fakeStats struct {
	mu         sync.Mutex
	secret     string
	traffic    map[string]map[string]int64
	kicked     [][]string
	reads      int
	failKick   bool
	breakClear bool
}

func (f *fakeStats) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != f.secret {
		http.Error(w, "unauthorized", http.StatusUnauthorized)

		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/traffic":
		f.reads++
		body, _ := json.Marshal(f.traffic)
		if r.URL.Query().Get("clear") == "1" {
			f.traffic = map[string]map[string]int64{}
			if f.breakClear {
				body = body[:len(body)/2]
			}
		}
		_, _ = w.Write(body)
	case r.Method == http.MethodPost && r.URL.Path == "/kick" && f.failKick:
		http.Error(w, "busy", http.StatusInternalServerError)
	case r.Method == http.MethodPost && r.URL.Path == "/kick":
		var ids []string
		_ = json.NewDecoder(r.Body).Decode(&ids)
		f.kicked = append(f.kicked, ids)
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func startFakeStats(t *testing.T, secret string) (*fakeStats, uint16) {
	t.Helper()

	f := &fakeStats{secret: secret, traffic: map[string]map[string]int64{}}
	srv := httptest.NewUnstartedServer(f)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.Listener = lis
	srv.Start()
	t.Cleanup(srv.Close)

	return f, uint16(lis.Addr().(*net.TCPAddr).Port)
}

func home(t *testing.T, obfs string) (string, *hysteriatypes.Config) {
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

	cfg := hysteriatypes.NewConfig().WithDefaultValues()
	cfg.Server.ListenPort = 4443
	cfg.Server.ObfsPassword = obfs
	if err := cfg.SaveToPath(filepath.Join(dir, hysteriatypes.ConfigFileName)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", dir)

	return dir, cfg
}

func stubBinary(t *testing.T) {
	t.Helper()
	stubScript(t, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile :; do sleep 0.1; done\n")
}

// stubScript puts script where the hysteria binary is looked for.
func stubScript(t *testing.T, script string) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "hysteria")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	saved := binaryName
	binaryName = path
	t.Cleanup(func() { binaryName = saved })
}

func TestInitRendersConfig(t *testing.T) {
	stubBinary(t)
	dir, cfg := home(t, "salt")

	s := NewHysteria()
	if err := s.Init(dir); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "hysteria_config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, want := range []string{
		`listen: ":4443"`,
		`cert: "` + filepath.Join(dir, "tls.crt") + `"`,
		"type: salamander",
		`password: "salt"`,
		`url: "http://127.0.0.1:` + itoa(cfg.API.AuthPort) + `/auth"`,
		`listen: "127.0.0.1:` + itoa(cfg.API.StatsPort) + `"`,
		`secret: "` + s.statsSecret + `"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("config lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "bandwidth:") {
		t.Error("bandwidth must be omitted when unset")
	}
	if s.ListenPort() != 4443 || s.info[2] != 1 || len(s.tlsPin) != 95 {
		t.Fatalf("info %x pin %q", s.info, s.tlsPin)
	}

	// No obfuscation, bandwidth set.
	dir, cfg = home(t, "")
	cfg.Server.Up, cfg.Server.Down = "100 mbps", "1 gbps"
	if err := cfg.SaveToPath(filepath.Join(dir, hysteriatypes.ConfigFileName)); err != nil {
		t.Fatal(err)
	}
	s = NewHysteria()
	if err := s.Init(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "hysteria_config.yaml"))
	if strings.Contains(string(raw), "salamander") || !strings.Contains(string(raw), `up: "100 mbps"`) {
		t.Fatalf("config:\n%s", raw)
	}
	if s.info[2] != 0 {
		t.Fatal("obfuscation flag set without a password")
	}
}

func itoa(v uint16) string {
	return strconv.Itoa(int(v))
}

// Rules: [RT-6].
func TestInitRequiresBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	saved := binaryName
	binaryName = "hysteria"
	t.Cleanup(func() { binaryName = saved })

	err := NewHysteria().Init(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), `"hysteria" binary is not on PATH`) {
		t.Fatalf("Init without the binary: %v", err)
	}
}

// Rules: [SL-1].
func TestAuthHook(t *testing.T) {
	s := NewHysteria()
	data, err := s.ParsePeerRequest([]byte(`{"uuid":"01020304-0506-0708-090a-0b0c0d0e0f10"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 16 || data[0] != 1 || data[15] != 16 {
		t.Fatalf("peer data: %x", data)
	}
	if _, err := s.AddPeer(data); err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(data)

	call := func(body string) authResponse {
		rec := httptest.NewRecorder()
		s.handleAuth(rec, httptest.NewRequest(http.MethodPost, "/auth", bytes.NewBufferString(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		var res authResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		return res
	}

	if res := call(`{"addr":"203.0.113.9:5000","auth":"01020304-0506-0708-090a-0b0c0d0e0f10","tx":0}`); !res.OK || res.ID != key {
		t.Fatalf("registered peer refused: %+v", res)
	}
	if res := call(`{"addr":"203.0.113.9:5000","auth":"nobody","tx":0}`); res.OK {
		t.Fatalf("unknown password accepted: %+v", res)
	}

	rec := httptest.NewRecorder()
	s.handleAuth(rec, httptest.NewRequest(http.MethodGet, "/auth", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET answered %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.handleAuth(rec, httptest.NewRequest(http.MethodPost, "/auth", bytes.NewBufferString("{")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("garbage answered %d", rec.Code)
	}
}

// Rules: [SL-1], [SL-16].
func TestPeersAndKick(t *testing.T) {
	s := NewHysteria()
	s.config = hysteriatypes.NewConfig().WithDefaultValues()
	s.statsSecret = "shh"
	stats, port := startFakeStats(t, s.statsSecret)
	s.config.API.StatsPort = port

	a, _ := s.ParsePeerRequest([]byte(`{"uuid":[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16]}`))
	b, _ := s.ParsePeerRequest([]byte(`{"uuid":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}`))
	for _, d := range [][]byte{a, b} {
		if _, err := s.AddPeer(d); err != nil {
			t.Fatal(err)
		}
	}
	keyA, keyB := base64.StdEncoding.EncodeToString(a), base64.StdEncoding.EncodeToString(b)

	stats.traffic[keyA] = map[string]int64{"tx": 100, "rx": 10}
	stats.traffic["stranger"] = map[string]int64{"tx": 5, "rx": 5}
	peers, err := s.Peers()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]int64{}
	for _, p := range peers {
		got[p.Key] = [2]int64{p.Upload, p.Download}
	}
	if len(got) != 2 || got[keyA] != [2]int64{100, 10} || got[keyB] != [2]int64{0, 0} {
		t.Fatalf("first read: %+v", got)
	}

	// Deltas accumulate across reads.
	stats.traffic[keyA] = map[string]int64{"tx": 1, "rx": 1}
	stats.traffic[keyB] = map[string]int64{"tx": 7, "rx": 3}
	peers, _ = s.Peers()
	got = map[string][2]int64{}
	for _, p := range peers {
		got[p.Key] = [2]int64{p.Upload, p.Download}
	}
	if got[keyA] != [2]int64{101, 11} || got[keyB] != [2]int64{7, 3} {
		t.Fatalf("second read: %+v", got)
	}

	if err := s.RemovePeer(a); err != nil {
		t.Fatal(err)
	}
	if s.HasPeer(a) || !s.HasPeer(b) || s.PeerCount() != 1 {
		t.Fatal("removal bookkeeping")
	}
	if len(stats.kicked) != 1 || stats.kicked[0][0] != keyA {
		t.Fatalf("kick: %v", stats.kicked)
	}

	s.statsSecret = "wrong"
	if _, err := s.Peers(); err == nil {
		t.Fatal("wrong secret accepted")
	}

	if _, err := s.AddPeer([]byte{1, 2}); err == nil {
		t.Fatal("short peer data accepted")
	}
}

// TestAFailedKickKeepsThePeerCounted: a removal whose kick fails refuses the
// peer's next connection at once, but keeps it registered and its open
// connection counted until a later removal kicks it.
//
// Rules: [SL-16].
func TestAFailedKickKeepsThePeerCounted(t *testing.T) {
	s := NewHysteria()
	s.config = hysteriatypes.NewConfig().WithDefaultValues()
	s.statsSecret = "shh"
	stats, port := startFakeStats(t, s.statsSecret)
	s.config.API.StatsPort = port

	uuid := "01020304-0506-0708-090a-0b0c0d0e0f10"
	data, _ := s.ParsePeerRequest([]byte(`{"uuid":"` + uuid + `"}`))
	if _, err := s.AddPeer(data); err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(data)

	stats.mu.Lock()
	stats.failKick = true
	stats.mu.Unlock()
	if err := s.RemovePeer(data); err == nil {
		t.Fatal("a failed kick reported success")
	}
	if !s.HasPeer(data) || s.PeerCount() != 1 {
		t.Fatal("the peer was forgotten before its kick succeeded")
	}
	if _, err := s.AddPeer(data); err == nil {
		t.Fatal("the peer was added again before its removal finished")
	}
	rec := httptest.NewRecorder()
	s.handleAuth(rec, httptest.NewRequest(http.MethodPost, "/auth",
		bytes.NewBufferString(`{"addr":"203.0.113.9:5000","auth":"`+uuid+`","tx":0}`)))
	if strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("a removed peer may connect again: %s", rec.Body.String())
	}

	stats.mu.Lock()
	stats.traffic[key] = map[string]int64{"tx": 40, "rx": 400}
	stats.mu.Unlock()
	peers, err := s.Peers()
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].Key != key || peers[0].Upload != 40 || peers[0].Download != 400 {
		t.Fatalf("want the open connection counted, got %+v", peers)
	}

	stats.mu.Lock()
	stats.failKick = false
	stats.mu.Unlock()
	if err := s.RemovePeer(data); err != nil {
		t.Fatal(err)
	}
	if s.HasPeer(data) || s.PeerCount() != 0 || len(stats.kicked) != 1 || stats.kicked[0][0] != key {
		t.Fatalf("not removed once the kick worked: kicked %v", stats.kicked)
	}
}

// TestABrokenTrafficReplyLosesNothing: the server clears its counters as it
// answers a clearing read, so a reply that arrives broken would lose what it
// carried. The counters read just before, without clearing, are kept then.
//
// Rules: [SL-2].
func TestABrokenTrafficReplyLosesNothing(t *testing.T) {
	s := NewHysteria()
	s.config = hysteriatypes.NewConfig().WithDefaultValues()
	s.statsSecret = "shh"
	stats, port := startFakeStats(t, s.statsSecret)
	s.config.API.StatsPort = port

	data, _ := s.ParsePeerRequest([]byte(`{"uuid":"01020304-0506-0708-090a-0b0c0d0e0f10"}`))
	if _, err := s.AddPeer(data); err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(data)

	stats.mu.Lock()
	stats.traffic[key] = map[string]int64{"tx": 40, "rx": 400}
	stats.breakClear = true
	stats.mu.Unlock()
	peers, err := s.Peers()
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].Upload != 40 || peers[0].Download != 400 {
		t.Fatalf("want 40 up and 400 down kept, got %+v", peers)
	}

	stats.mu.Lock()
	stats.traffic[key] = map[string]int64{"tx": 1, "rx": 2}
	stats.breakClear = false
	stats.mu.Unlock()
	if peers, err = s.Peers(); err != nil || peers[0].Upload != 41 || peers[0].Download != 402 {
		t.Fatalf("want 41 up and 402 down, got %+v, %v", peers, err)
	}
}

// Rules: [RT-3].
func TestStartStop(t *testing.T) {
	stubBinary(t)
	dir, cfg := home(t, "")

	s := NewHysteria()
	if err := s.Init(dir); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}

	// The auth hook answers while the server runs.
	res, err := http.Post("http://127.0.0.1:"+itoa(cfg.API.AuthPort)+"/auth", "application/json",
		bytes.NewBufferString(`{"addr":"a","auth":"b","tx":0}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("auth hook: %d", res.StatusCode)
	}

	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := http.Post("http://127.0.0.1:"+itoa(cfg.API.AuthPort)+"/auth", "application/json", nil); err == nil {
		t.Fatal("auth hook still up after Stop")
	}
	if err := NewHysteria().Stop(); err == nil {
		t.Fatal("Stop before Start must fail")
	}
}

// TestDoneWhenTheServerOrTheHookEnds: the server dying, or the authentication
// hook no longer serving (the server then admits no one), closes Done, on
// which the start command stops the node. Hysteria's AddPeer never reaches
// the server, so without it the node kept admitting paying clients.
//
// Rules: [RT-15].
func TestDoneWhenTheServerOrTheHookEnds(t *testing.T) {
	start := func(t *testing.T) (*Hysteria, string) {
		pidFile := filepath.Join(t.TempDir(), "pid")
		stubScript(t, daemontest.Script(pidFile))
		dir, _ := home(t, "")
		s := NewHysteria()
		if err := s.Init(dir); err != nil {
			t.Fatal(err)
		}
		if err := s.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Stop() })

		return s, pidFile
	}

	t.Run("the server dies", func(t *testing.T) {
		s, pidFile := start(t)
		daemontest.Crash(t, pidFile, s.Done())
	})
	t.Run("the hook stops serving", func(t *testing.T) {
		s, _ := start(t)
		_ = s.auth.Close()
		daemontest.Closes(t, s.Done(), "the hook stopped serving")
	})
}

// Rules: [CT-3], [CT-4].
func TestHandshakePayload(t *testing.T) {
	stubBinary(t)
	dir, _ := home(t, "salt")

	s := NewHysteria()
	if err := s.Init(dir); err != nil {
		t.Fatal(err)
	}
	payload, err := s.HandshakePayload(nil)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(payload)
	want := `{"metadata":[{"port":4443,"tls_pin":"` + s.tlsPin + `","obfs_password":"salt"}]}`
	if string(out) != want {
		t.Fatalf("payload:\n got %s\nwant %s", out, want)
	}
	// encoding/json escapes < and >; compare decoded.
	pub, _ := json.Marshal(s.PublicMetadata())
	var entries []map[string]interface{}
	if err := json.Unmarshal(pub, &entries); err != nil || len(entries) != 1 ||
		entries[0]["port"] != float64(0) || entries[0]["tls_pin"] != "" || entries[0]["obfs_password"] != "<redacted>" {
		t.Fatalf("public metadata must blank the port and pin and hide the password: %s", pub)
	}
	if s.Name() != "hysteria2" || s.Type() != 6 {
		t.Fatalf("identity: %s/%d", s.Name(), s.Type())
	}
}

// TestServerArgs: hysteria's info level logs every client's address, so the
// node asks for it only when it runs at debug itself; the update check, an
// outside call nobody chose, is always off.
//
// Rules: [PV-2].
func TestServerArgs(t *testing.T) {
	t.Cleanup(func() { common.SetVerbose(false) })

	for _, tc := range []struct {
		verbose bool
		want    string
	}{
		{verbose: false, want: "server --disable-update-check --log-level error -c /h/c.yaml"},
		{verbose: true, want: "server --disable-update-check --log-level info -c /h/c.yaml"},
	} {
		common.SetVerbose(tc.verbose)
		if got := strings.Join(serverArgs("/h/c.yaml"), " "); got != tc.want {
			t.Errorf("verbose %v: got %q, want %q", tc.verbose, got, tc.want)
		}
	}
}

// aclRule is the shape of one ACL line hysteria accepts:
// outbound(address[, proto/port[, hijack]]).
var aclRule = regexp.MustCompile(`^(\w+)\s*\(([^,]+)(?:,([^,]+))?(?:,([^,]+))?\)$`)

// aclRules is the inline ACL of the rendered config, in order.
func aclRules(t *testing.T, dir string) []string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(dir, "hysteria_config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, block, ok := strings.Cut(string(raw), "\nacl:\n  inline:\n")
	if !ok {
		t.Fatalf("no inline ACL:\n%s", raw)
	}

	var rules []string
	for _, line := range strings.Split(strings.TrimRight(block, "\n"), "\n") {
		item, ok := strings.CutPrefix(line, "    - ")
		if !ok {
			t.Fatalf("the ACL must be the last block; found %q", line)
		}
		rule, err := strconv.Unquote(item)
		if err != nil || !aclRule.MatchString(rule) {
			t.Fatalf("ACL line %q is not a hysteria rule (%v)", item, err)
		}
		rules = append(rules, rule)
	}

	return rules
}

// TestInitRendersEgressPolicy: a client cannot reach the blocked networks
// (the auth hook and the statistics API on loopback among them), localhost by
// name, or port 25 unless the operator allows it.
//
// Rules: [EG-1], [EG-4], [EG-5].
func TestInitRendersEgressPolicy(t *testing.T) {
	stubBinary(t)
	t.Cleanup(func() { common.SetEgress(common.Egress{}) })

	dir, _ := home(t, "")
	if err := NewHysteria().Init(dir); err != nil {
		t.Fatal(err)
	}

	want := []string{"reject(suffix:localhost)"}
	for _, cidr := range common.BlockedNetworks() {
		want = append(want, "reject("+cidr+")")
	}
	want = append(want, "reject(all, tcp/25)")
	if got := aclRules(t, dir); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ACL:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	common.SetEgress(common.Egress{AllowSMTP: true})
	if err := NewHysteria().Init(dir); err != nil {
		t.Fatal(err)
	}
	if got := aclRules(t, dir); len(got) != len(want)-1 || strings.Contains(strings.Join(got, " "), "tcp/25") {
		t.Fatalf("allow_smtp: ACL %v", got)
	}
}
