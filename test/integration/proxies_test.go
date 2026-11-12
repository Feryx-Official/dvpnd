// SPDX-License-Identifier: Apache-2.0

//go:build integration

package integration

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
	"github.com/Feryx-Official/dvpnd/v9/services/hysteria"
	hysteriatypes "github.com/Feryx-Official/dvpnd/v9/services/hysteria/types"
	"github.com/Feryx-Official/dvpnd/v9/services/v2ray"
	v2raytypes "github.com/Feryx-Official/dvpnd/v9/services/v2ray/types"
	"github.com/Feryx-Official/dvpnd/v9/services/xray"
	xraytypes "github.com/Feryx-Official/dvpnd/v9/services/xray/types"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// proxyCase is one proxy protocol: how the node's configuration is written,
// and how a client is configured from the handshake payload alone, as a
// client app does.
type proxyCase struct {
	name string
	// daemon is the protocol binary's process name.
	daemon string
	// service writes the protocol's configuration into home and returns the
	// service with the loopback ports of the daemon's control API, which no
	// client may reach.
	service func(t *testing.T, home string) (types.Service, []uint16)
	// client writes a client configuration into dir from the handshake
	// entry and returns the command that runs it with a SOCKS5 listener on
	// socksPort.
	client func(t *testing.T, dir string, entry map[string]interface{}, uuid string, socksPort int) []string
}

func proxyCases() []proxyCase {
	return []proxyCase{
		{name: "xray-reality", daemon: "xray", service: xrayService(xraytypes.SecurityReality), client: xrayClient},
		{name: "xray-tls", daemon: "xray", service: xrayService(xraytypes.SecurityTLS), client: xrayClient},
		{name: "v2ray", daemon: "v2ray", service: v2rayService, client: v2rayClient},
		{name: "hysteria2", daemon: "hysteria", service: hysteriaService, client: hysteriaClient},
	}
}

// TestProxies runs each proxy protocol as the node does: the daemon started
// by the service as the proxy account, a peer added by the handshake, a real
// client configured from the handshake payload. Under the default policy and
// with mail allowed, the client must reach the internet and the node API and
// nothing else; the node must count its traffic; once the peer is removed it
// must reach nothing; once the service stops nothing of it may be left.
//
// Rules: [EG-12], [RT-1], [RT-7], [SL-1], [SL-16].
func TestProxies(t *testing.T) {
	requireEnv(t)

	for _, pc := range proxyCases() {
		if _, err := exec.LookPath(pc.daemon); err != nil {
			t.Run(pc.name, func(t *testing.T) { t.Skipf("%s is not installed here", pc.daemon) })
			continue
		}
		t.Run(pc.name, func(t *testing.T) {
			for _, allowSMTP := range []bool{false, true} {
				t.Run(fmt.Sprintf("allow_smtp=%v", allowSMTP), func(t *testing.T) {
					runProxy(t, pc, allowSMTP)
				})
			}
		})
	}
}

func runProxy(t *testing.T, pc proxyCase, allowSMTP bool) {
	home := t.TempDir()
	writeTLS(t, home)

	common.SetEgress(common.Egress{APIPort: apiPort, AllowSMTP: allowSMTP})
	rt, err := common.PrepareRuntime(home, os.Geteuid(), true)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Proxy == nil {
		t.Fatalf("the %s account is missing: the daemons would run as root", common.ProxyUserName)
	}
	common.SetRuntime(rt)

	svc, control := pc.service(t, home)
	if err := svc.Init(home); err != nil {
		t.Fatal("Init:", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatal("Start:", err)
	}
	running := true
	t.Cleanup(func() {
		if running {
			_ = svc.Stop()
		}
	})

	// The daemon runs as the proxy account, never as root.
	var procs map[int]int
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if procs = processes(t, pc.daemon, rt.Dir); len(procs) == 1 || time.Now().After(deadline) {
			break
		}
	}
	if len(procs) != 1 {
		out, _ := exec.Command("ps", "-eo", "pid,user,stat,comm,args").CombinedOutput()
		t.Fatalf("want one %s daemon reading from %s, found %v\n%s", pc.daemon, rt.Dir, procs, out)
	}
	for pid, uid := range procs {
		if uid != int(rt.Proxy.UID) {
			t.Fatalf("%s (pid %d) runs as uid %d, not %s", pc.daemon, pid, uid, common.ProxyUserName)
		}
	}

	// The handshake, as the node runs it for a client, right after Start: a
	// node takes handshakes as soon as it is up, before the daemon may be.
	uuid := newUUID(t)
	data, err := svc.ParsePeerRequest([]byte(fmt.Sprintf(`{"uuid":%q}`, uuid)))
	if err != nil {
		t.Fatal("ParsePeerRequest:", err)
	}
	result, err := svc.AddPeer(data)
	if err != nil {
		t.Fatal("AddPeer:", err)
	}
	entry := handshakeEntry(t, svc, result)

	socksPort := freePort(t)
	argv := pc.client(t, t.TempDir(), entry, uuid, socksPort)
	client := startClient(t, argv[0], argv[1:]...)
	dial := socks5(net.JoinHostPort("127.0.0.1", strconv.Itoa(socksPort)))

	public := net.JoinHostPort(env.public, strconv.Itoa(targetPort))
	waitReach(t, dial, public, 30*time.Second)

	view := clientView(allowSMTP)
	for _, port := range control {
		view = append(view, reach{net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), false})
	}
	checkReach(t, dial, view)

	// The node bills on these counters.
	peers, err := svc.Peers()
	if err != nil {
		t.Fatal("Peers:", err)
	}
	if len(peers) != 1 || peers[0].Upload+peers[0].Download == 0 {
		t.Errorf("want one peer with its traffic counted, got %+v", peers)
	}

	if err := svc.RemovePeer(data); err != nil {
		t.Fatal("RemovePeer:", err)
	}
	waitBlocked(t, dial, public, 20*time.Second)
	if n := svc.PeerCount(); n != 0 {
		t.Errorf("PeerCount after RemovePeer: %d", n)
	}

	// The same peer added again, as a later session with the client's
	// UUID is, starts from zero: the node bills on the counters from the
	// moment it adds a peer, never on what an earlier session moved.
	_ = client.Process.Kill()
	_ = client.Wait()
	if _, err := svc.AddPeer(data); err != nil {
		t.Fatal("AddPeer again:", err)
	}
	if peers, err = svc.Peers(); err != nil {
		t.Fatal("Peers:", err)
	}
	if len(peers) != 1 || peers[0].Upload != 0 || peers[0].Download != 0 {
		t.Errorf("want the peer added again to start from zero, got %+v", peers)
	}
	if err := svc.RemovePeer(data); err != nil {
		t.Fatal("RemovePeer:", err)
	}

	running = false
	if err := stopWatched(t, svc); err != nil {
		t.Error("Stop:", err)
	}
	if left := processes(t, pc.daemon, rt.Dir); len(left) != 0 {
		t.Errorf("%s still running after Stop: %v", pc.daemon, left)
	}
	noChainsLeft(t)
}

// handshakeEntry is the first metadata entry of the handshake payload, as a
// client decodes it from JSON.
func handshakeEntry(t *testing.T, svc types.Service, result []byte) map[string]interface{} {
	t.Helper()

	payload, err := svc.HandshakePayload(result)
	if err != nil {
		t.Fatal("HandshakePayload:", err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Metadata []map[string]interface{} `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil || len(decoded.Metadata) == 0 {
		t.Fatalf("handshake payload %s: %v", raw, err)
	}

	return decoded.Metadata[0]
}

func newUUID(t *testing.T) string {
	t.Helper()

	var id [common.UUIDLen]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	id[6] = id[6]&0x0f | 0x40 // version 4
	id[8] = id[8]&0x3f | 0x80 // RFC 4122 variant

	return common.FormatUUID(id)
}

func freePort(t *testing.T) int {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	return l.Addr().(*net.TCPAddr).Port
}

func writeJSON(t *testing.T, path string, v interface{}) {
	t.Helper()

	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func port(t *testing.T, entry map[string]interface{}) int {
	t.Helper()

	switch v := entry["port"].(type) {
	case float64:
		return int(v)
	case string:
		p, err := strconv.Atoi(v)
		if err == nil {
			return p
		}
	}
	t.Fatalf("handshake entry without a port: %v", entry)

	return 0
}

func str(entry map[string]interface{}, key string) string {
	s, _ := entry[key].(string)
	return s
}

func socksInbound(socksPort int) []interface{} {
	return []interface{}{map[string]interface{}{
		"listen": "127.0.0.1", "port": socksPort, "protocol": "socks",
		"settings": map[string]interface{}{"udp": false},
	}}
}

func xrayService(security string) func(t *testing.T, home string) (types.Service, []uint16) {
	return func(t *testing.T, home string) (types.Service, []uint16) {
		t.Helper()

		c := xraytypes.NewConfig().WithDefaultValues()
		c.VLESS.Security = security
		if err := c.SaveToPath(filepath.Join(home, xraytypes.ConfigFileName)); err != nil {
			t.Fatal(err)
		}

		return xray.NewXRay(), []uint16{c.API.Port}
	}
}

// xrayClient is a VLESS outbound with Vision, over REALITY with the node's
// public key and short id, or over TLS with the node's certificate pinned.
func xrayClient(t *testing.T, dir string, entry map[string]interface{}, uuid string, socksPort int) []string {
	t.Helper()

	user := map[string]interface{}{"id": uuid, "encryption": "none"}
	if flow, _ := entry["flow"].(float64); int(flow) == types.FlowVision {
		user["flow"] = "xtls-rprx-vision"
	}
	stream := map[string]interface{}{"network": "tcp"}
	switch sec, _ := entry["transport_security"].(float64); int(sec) {
	case types.TransportSecurityReality:
		stream["security"] = "reality"
		stream["realitySettings"] = map[string]interface{}{
			"serverName":  str(entry, "reality_server_name"),
			"fingerprint": str(entry, "reality_fingerprint"),
			"publicKey":   str(entry, "reality_public_key"),
			"shortId":     str(entry, "reality_short_id"),
		}
	case types.TransportSecurityTLS:
		stream["security"] = "tls"
		stream["tlsSettings"] = map[string]interface{}{
			"serverName":           "dvpnd",
			"pinnedPeerCertSha256": str(entry, "tls_pin"),
		}
	default:
		t.Fatalf("xray handshake entry with security %v", entry["transport_security"])
	}

	path := filepath.Join(dir, "xray-client.json")
	writeJSON(t, path, map[string]interface{}{
		"log":      map[string]interface{}{"loglevel": "warning"},
		"inbounds": socksInbound(socksPort),
		"outbounds": []interface{}{map[string]interface{}{
			"protocol": "vless",
			"settings": map[string]interface{}{"vnext": []interface{}{map[string]interface{}{
				"address": "127.0.0.1", "port": port(t, entry), "users": []interface{}{user},
			}}},
			"streamSettings": stream,
		}},
	})

	return []string{"xray", "run", "-config", path}
}

func v2rayService(t *testing.T, home string) (types.Service, []uint16) {
	t.Helper()

	c := v2raytypes.NewConfig().WithDefaultValues()
	if err := c.SaveToPath(filepath.Join(home, v2raytypes.ConfigFileName)); err != nil {
		t.Fatal(err)
	}

	return v2ray.NewV2Ray(), []uint16{c.API.Port}
}

// v2rayClient is a VMess outbound with AEAD headers over plain TCP.
func v2rayClient(t *testing.T, dir string, entry map[string]interface{}, uuid string, socksPort int) []string {
	t.Helper()

	if sec, _ := entry["transport_security"].(float64); int(sec) != types.TransportSecurityNone {
		t.Fatalf("v2ray handshake entry with security %v; the test configures none", entry["transport_security"])
	}

	path := filepath.Join(dir, "v2ray-client.json")
	writeJSON(t, path, map[string]interface{}{
		"log":      map[string]interface{}{"loglevel": "warning"},
		"inbounds": socksInbound(socksPort),
		"outbounds": []interface{}{map[string]interface{}{
			"protocol": "vmess",
			"settings": map[string]interface{}{"vnext": []interface{}{map[string]interface{}{
				"address": "127.0.0.1", "port": port(t, entry),
				"users": []interface{}{map[string]interface{}{"id": uuid, "alterId": 0, "security": "auto"}},
			}}},
		}},
	})

	return []string{"v2ray", "run", "-c", path}
}

func hysteriaService(t *testing.T, home string) (types.Service, []uint16) {
	t.Helper()

	c := hysteriatypes.NewConfig().WithDefaultValues()
	if err := c.SaveToPath(filepath.Join(home, hysteriatypes.ConfigFileName)); err != nil {
		t.Fatal(err)
	}

	return hysteria.NewHysteria(), []uint16{c.API.AuthPort, c.API.StatsPort}
}

// hysteriaClient authenticates with the uuid and pins the certificate from
// the handshake entry.
func hysteriaClient(t *testing.T, dir string, entry map[string]interface{}, uuid string, socksPort int) []string {
	t.Helper()

	pin := str(entry, "tls_pin")
	if pin == "" {
		t.Fatalf("hysteria2 handshake entry without a pin: %v", entry)
	}
	conf := fmt.Sprintf("server: 127.0.0.1:%d\nauth: %s\ntls:\n  sni: dvpnd\n  insecure: true\n  pinSHA256: %q\n",
		port(t, entry), uuid, pin)
	if obfs := str(entry, "obfs_password"); obfs != "" {
		conf += fmt.Sprintf("obfs:\n  type: salamander\n  salamander:\n    password: %q\n", obfs)
	}
	conf += fmt.Sprintf("socks5:\n  listen: 127.0.0.1:%d\n", socksPort)

	path := filepath.Join(dir, "hysteria-client.yaml")
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}

	return []string{"hysteria", "client", "--disable-update-check", "--log-level", "warn", "-c", path}
}
