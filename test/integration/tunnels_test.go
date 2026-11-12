// SPDX-License-Identifier: Apache-2.0

//go:build integration

package integration

import (
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Feryx-Official/dvpnd/v9/services/amneziawg"
	awgtypes "github.com/Feryx-Official/dvpnd/v9/services/amneziawg/types"
	"github.com/Feryx-Official/dvpnd/v9/services/common"
	"github.com/Feryx-Official/dvpnd/v9/services/openvpn"
	ovpntypes "github.com/Feryx-Official/dvpnd/v9/services/openvpn/types"
	"github.com/Feryx-Official/dvpnd/v9/services/wireguard"
	wgtypes "github.com/Feryx-Official/dvpnd/v9/services/wireguard/types"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// The client's network namespace and the veth pair that joins it to the
// host. The client reaches the node's listener on hostSide; everything else
// it routes into its tunnel.
const (
	clientNS   = "dvit-cli"
	hostVeth   = "dvit-host"
	clientVeth = "dvit-peer"
	hostSide   = "172.31.250.1"
	clientSide = "172.31.250.2"
)

// tunnelCase is one tunnel protocol and the client tiers it offers.
type tunnelCase struct {
	name string
	// daemon is the process that must run as the proxy account, if any.
	daemon string
	// service writes the configuration into home and returns the service.
	service func(t *testing.T, home string) types.Service
	// tiers are the peer requests a client may send.
	tiers []tunnelTier
}

// tunnelTier builds a peer request and, from the handshake payload, brings
// up a client interface in the client namespace and returns its name.
type tunnelTier struct {
	name    string
	request func(t *testing.T) (req []byte, secret string)
	client  func(t *testing.T, dir, secret string, payload []byte) string
}

func tunnelCases() []tunnelCase {
	return []tunnelCase{
		{
			name: "wireguard", service: wireguardService,
			tiers: []tunnelTier{{name: "default", request: wgRequest(""), client: wireguardClient}},
		},
		{
			name: "amneziawg", service: amneziawgService,
			tiers: []tunnelTier{
				{name: "default", request: wgRequest(""), client: amneziawgClient},
				{name: "v3", request: wgRequest(`,"awg_version":3`), client: amneziawgClient},
			},
		},
		{
			name: "openvpn", daemon: "openvpn", service: openvpnService,
			tiers: []tunnelTier{{name: "default", request: uuidRequest, client: openvpnClient}},
		},
	}
}

// TestTunnels runs each tunnel protocol as the node does, with a client in
// its own network namespace configured from the handshake payload alone.
// Through the tunnel the client must reach the internet and the node API and
// nothing else, under the default policy and with mail allowed; a control
// step then takes the node's chains out of the path and the same client
// reaches the private network and the host, which shows the network under
// test would have let it through. The node must count the traffic, cut the
// client off when the peer is removed, and leave no rules behind.
//
// Rules: [EG-12], [RT-7], [SL-1], [SL-16].
func TestTunnels(t *testing.T) {
	requireEnv(t)
	if os.Getenv("DVPND_IT_TUNNELS") != "1" {
		t.Skip("tunnel clients need a privileged container (run.sh's tunnels stage)")
	}

	addMasquerade(t)
	for _, tc := range tunnelCases() {
		t.Run(tc.name, func(t *testing.T) {
			for _, tier := range tc.tiers {
				for _, allowSMTP := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/allow_smtp=%v", tier.name, allowSMTP), func(t *testing.T) {
						runTunnel(t, tc, tier, allowSMTP)
					})
				}
			}
		})
	}
}

func runTunnel(t *testing.T, tc tunnelCase, tier tunnelTier, allowSMTP bool) {
	home := t.TempDir()
	writeTLS(t, home)
	makeClientNS(t)

	common.SetEgress(common.Egress{APIPort: apiPort, AllowSMTP: allowSMTP})
	rt, err := common.PrepareRuntime(home, os.Geteuid(), tc.daemon != "")
	if err != nil {
		t.Fatal(err)
	}
	common.SetRuntime(rt)

	svc := tc.service(t, home)
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

	req, secret := tier.request(t)
	data, err := svc.ParsePeerRequest(req)
	if err != nil {
		t.Fatal("ParsePeerRequest:", err)
	}
	result, err := svc.AddPeer(data)
	if err != nil {
		t.Fatal("AddPeer:", err)
	}
	payload, err := svc.HandshakePayload(result)
	if err != nil {
		t.Fatal("HandshakePayload:", err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	clientIf := tier.client(t, t.TempDir(), secret, raw)
	for _, cidr := range []string{env.public + "/24", env.private + "/24"} {
		_, n, _ := net.ParseCIDR(cidr)
		mustRun(t, "ip", "-n", clientNS, "route", "replace", n.String(), "dev", clientIf)
	}

	public := net.JoinHostPort(env.public, strconv.Itoa(targetPort))
	waitReach(t, nsDial(t), public, 30*time.Second)

	if tc.daemon != "" && rt.Proxy != nil {
		checkDropped(t, tc.daemon, rt)
	}

	// Through the tunnel: the names are resolved by the client, so the
	// loopback names only show the client's own loopback; they are left out.
	view := []reach{}
	for _, c := range clientView(allowSMTP) {
		host, _, _ := net.SplitHostPort(c.target)
		if net.ParseIP(host) == nil || net.ParseIP(host).IsLoopback() {
			continue
		}
		view = append(view, c)
	}
	checkReach(t, nsDialAll(t, view), view)

	peers, err := svc.Peers()
	if err != nil {
		t.Fatal("Peers:", err)
	}
	if len(peers) != 1 || peers[0].Upload+peers[0].Download == 0 {
		t.Errorf("want one peer with its traffic counted, got %+v", peers)
	}

	// Control: with the node's chains out of the path the same client
	// reaches the private network and the host's other service.
	removeJumps(t)
	control := []reach{
		{net.JoinHostPort(env.private, strconv.Itoa(targetPort)), true},
		{net.JoinHostPort(env.self, strconv.Itoa(hostPort)), true},
	}
	checkReach(t, nsDialAll(t, control), control)

	if err := svc.RemovePeer(data); err != nil {
		t.Fatal("RemovePeer:", err)
	}
	waitBlocked(t, nsDial(t), public, 30*time.Second)

	running = false
	if err := stopWatched(t, svc); err != nil {
		t.Error("Stop:", err)
	}
	noChainsLeft(t)
}

// removeJumps deletes every jump into the node's chains from FORWARD and
// INPUT; Stop still removes the chains.
func removeJumps(t *testing.T) {
	t.Helper()

	for _, builtin := range []string{"FORWARD", "INPUT"} {
		out, err := exec.Command("iptables", "-S", builtin).Output()
		if err != nil {
			t.Fatalf("iptables -S %s: %v", builtin, err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "-A ") && strings.Contains(line, "-j DVPND-") {
				mustRun(t, "iptables", append([]string{"-D"}, strings.Fields(line)[1:]...)...)
			}
		}
	}
}

// checkDropped waits for the daemon to switch to the proxy account, which
// OpenVPN does once its tunnel is up.
func checkDropped(t *testing.T, daemon string, rt common.Runtime) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for {
		procs := processes(t, daemon, rt.Dir)
		dropped := len(procs) == 1
		for _, uid := range procs {
			dropped = dropped && uid == int(rt.Proxy.UID)
		}
		if dropped {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not drop to %s: %v", daemon, common.ProxyUserName, procs)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// nsDial dials one target from the client namespace.
func nsDial(t *testing.T) dialFunc {
	return func(target string) (string, error) {
		return nsDialAll(t, []reach{{target, true}})(target)
	}
}

// nsDialAll dials every target from the client namespace in one run.
func nsDialAll(t *testing.T, cases []reach) dialFunc {
	return runDial(t, []string{"ip", "netns", "exec", clientNS}, nil, cases)
}

// makeClientNS creates the client namespace, joined to the host by a veth
// pair, and removes it when the test ends.
func makeClientNS(t *testing.T) {
	t.Helper()

	_ = exec.Command("ip", "netns", "del", clientNS).Run()
	_ = exec.Command("ip", "link", "del", hostVeth).Run()
	mustRun(t, "ip", "netns", "add", clientNS)
	t.Cleanup(func() {
		_ = exec.Command("ip", "netns", "del", clientNS).Run()
		_ = exec.Command("ip", "link", "del", hostVeth).Run()
	})
	mustRun(t, "ip", "link", "add", hostVeth, "type", "veth", "peer", "name", clientVeth)
	mustRun(t, "ip", "link", "set", clientVeth, "netns", clientNS)
	mustRun(t, "ip", "addr", "add", hostSide+"/30", "dev", hostVeth)
	mustRun(t, "ip", "link", "set", hostVeth, "up")
	mustRun(t, "ip", "-n", clientNS, "addr", "add", clientSide+"/30", "dev", clientVeth)
	mustRun(t, "ip", "-n", clientNS, "link", "set", clientVeth, "up")
	mustRun(t, "ip", "-n", clientNS, "link", "set", "lo", "up")
}

// addMasquerade lets forwarded traffic into the private network come back,
// as it would from a provider network behind the uplink. Without it the
// private target could not answer a tunnel client, and the test could not
// tell the node's block from a missing route.
func addMasquerade(t *testing.T) {
	t.Helper()

	iface := interfaceFor(t, net.ParseIP(env.private))
	rule := []string{"POSTROUTING", "-o", iface, "-j", "MASQUERADE"}
	mustRun(t, "iptables", append([]string{"-t", "nat", "-I"}, rule...)...)
	t.Cleanup(func() { _ = exec.Command("iptables", append([]string{"-t", "nat", "-D"}, rule...)...).Run() })
}

// interfaceFor is the host's interface on the network that holds ip.
func interfaceFor(t *testing.T, ip net.IP) string {
	t.Helper()

	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range ifaces {
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.Contains(ip) {
				return i.Name
			}
		}
	}
	t.Fatalf("no interface on the network of %s", ip)

	return ""
}

func mustRun(t *testing.T, name string, args ...string) {
	t.Helper()

	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func uuidRequest(t *testing.T) ([]byte, string) {
	return []byte(fmt.Sprintf(`{"uuid":%q}`, newUUID(t))), ""
}

// wgRequest is a peer request with a fresh key; extra is appended to the
// JSON object.
func wgRequest(extra string) func(t *testing.T) ([]byte, string) {
	return func(t *testing.T) ([]byte, string) {
		key, err := wgtypes.NewPrivateKey()
		if err != nil {
			t.Fatal(err)
		}

		return []byte(fmt.Sprintf(`{"public_key":%q%s}`, key.Public().String(), extra)), key.String()
	}
}

func wireguardService(t *testing.T, home string) types.Service {
	t.Helper()

	c := wgtypes.NewConfig().WithDefaultValues()
	if err := c.SaveToPath(filepath.Join(home, wgtypes.ConfigFileName)); err != nil {
		t.Fatal(err)
	}
	svc, err := wireguard.NewService(nil)
	if err != nil {
		t.Fatal(err)
	}

	return svc
}

// tunnelPayload is the part of a WireGuard or AmneziaWG handshake payload a
// client reads.
type tunnelPayload struct {
	Addrs    []string                 `json:"addrs"`
	Metadata []map[string]interface{} `json:"metadata"`
}

func decodeTunnelPayload(t *testing.T, raw []byte) (tunnelPayload, map[string]interface{}) {
	t.Helper()

	var p tunnelPayload
	if err := json.Unmarshal(raw, &p); err != nil || len(p.Metadata) == 0 || len(p.Addrs) == 0 {
		t.Fatalf("handshake payload %s: %v", raw, err)
	}

	return p, p.Metadata[0]
}

// ipv4Addr is the client's IPv4 tunnel address from the payload.
func ipv4Addr(t *testing.T, p tunnelPayload) string {
	t.Helper()

	for _, a := range p.Addrs {
		if ip, _, err := net.ParseCIDR(a); err == nil && ip.To4() != nil {
			return a
		}
	}
	t.Fatalf("no IPv4 tunnel address in %v", p.Addrs)

	return ""
}

func number(entry map[string]interface{}, key string) string {
	v, _ := entry[key].(float64)
	return strconv.FormatFloat(v, 'f', 0, 64)
}

// wireguardClient brings up a kernel WireGuard interface in the client
// namespace.
func wireguardClient(t *testing.T, dir, secret string, raw []byte) string {
	t.Helper()

	p, entry := decodeTunnelPayload(t, raw)
	keyFile := filepath.Join(dir, "client.key")
	if err := os.WriteFile(keyFile, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}

	const iface = "wgc0"
	mustRun(t, "ip", "-n", clientNS, "link", "add", iface, "type", "wireguard")
	mustRun(t, "ip", "netns", "exec", clientNS, "wg", "set", iface, "private-key", keyFile,
		"peer", str(entry, "public_key"), "endpoint", net.JoinHostPort(hostSide, number(entry, "port")),
		"allowed-ips", "0.0.0.0/0", "persistent-keepalive", "5")
	mustRun(t, "ip", "-n", clientNS, "addr", "add", ipv4Addr(t, p), "dev", iface)
	mustRun(t, "ip", "-n", clientNS, "link", "set", iface, "up")

	return iface
}

func amneziawgService(t *testing.T, home string) types.Service {
	t.Helper()

	c := awgtypes.NewConfig().WithDefaultValues()
	if err := c.SaveToPath(filepath.Join(home, awgtypes.ConfigFileName)); err != nil {
		t.Fatal(err)
	}
	svc, err := amneziawg.NewService(nil)
	if err != nil {
		t.Fatal(err)
	}

	return svc
}

// amneziawgClient brings up the userspace AmneziaWG engine in the client
// namespace with the parameters of the payload, as the client apps set them:
// their own junk packets, the node's sizes, headers and signature packets,
// and on the 3.1 tier the header protection key, trailers and MTU.
func amneziawgClient(t *testing.T, dir, secret string, raw []byte) string {
	t.Helper()

	p, entry := decodeTunnelPayload(t, raw)

	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nJc = 5\nJmin = 64\nJmax = 256\n", secret)
	for _, k := range []string{"s1", "s2", "s3", "s4", "h1", "h2", "h3", "h4"} {
		fmt.Fprintf(&b, "%s = %s\n", strings.ToUpper(k), number(entry, k))
	}
	for _, k := range []string{"i1", "i2", "i3", "i4", "i5"} {
		if v := str(entry, k); v != "" {
			fmt.Fprintf(&b, "%s = %s\n", strings.ToUpper(k), v)
		}
	}
	mtu := ""
	if number(entry, "awg_version") == "3" {
		trailers := "off"
		if on, _ := entry["random_trailers"].(bool); on {
			trailers = "on"
		}
		fmt.Fprintf(&b, "HeaderProtectionKey = %s\nRandomTrailers = %s\nContentPaddingAddition = 0-32\n",
			str(entry, "header_protection_key"), trailers)
		mtu = number(entry, "mtu")
	}
	fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = 0.0.0.0/0\nPersistentKeepalive = 5\n",
		str(entry, "public_key"), net.JoinHostPort(hostSide, number(entry, "port")))

	conf := filepath.Join(dir, "client.conf")
	if err := os.WriteFile(conf, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	// In the foreground, so the engine ends with the test.
	const iface = "awgc0"
	socket := "/var/run/amneziawg/" + iface + ".sock"
	_ = os.Remove(socket) // left by the engine of an earlier test, which was killed
	startClient(t, "ip", "netns", "exec", clientNS, "amneziawg-go", "-f", iface)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the AmneziaWG engine did not open", socket)
		}
	}
	mustRun(t, "ip", "netns", "exec", clientNS, "awg", "setconf", iface, conf)
	mustRun(t, "ip", "-n", clientNS, "addr", "add", ipv4Addr(t, p), "dev", iface)
	if mtu != "" && mtu != "0" {
		mustRun(t, "ip", "-n", clientNS, "link", "set", iface, "mtu", mtu)
	}
	mustRun(t, "ip", "-n", clientNS, "link", "set", iface, "up")

	return iface
}

func openvpnService(t *testing.T, home string) types.Service {
	t.Helper()

	c := ovpntypes.NewConfig().WithDefaultValues()
	if err := c.SaveToPath(filepath.Join(home, ovpntypes.ConfigFileName)); err != nil {
		t.Fatal(err)
	}

	return openvpn.NewOpenVPN()
}

// openvpnClient runs the OpenVPN client in the client namespace with the
// profile a client app builds from the payload: the CA, its own certificate
// and key, and the tls-crypt key, with the settings the server pins.
func openvpnClient(t *testing.T, dir, _ string, raw []byte) string {
	t.Helper()

	var p struct {
		Metadata []struct {
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
			CA       []byte `json:"ca"`
			TLS      []byte `json:"tls"`
		} `json:"metadata"`
		Cert []byte `json:"cert"`
		Key  []byte `json:"key"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || len(p.Metadata) == 0 {
		t.Fatalf("openvpn handshake payload %s: %v", raw, err)
	}
	m := p.Metadata[0]

	proto := "udp"
	if m.Protocol == "tcp" {
		proto = "tcp-client"
	}
	tlsCrypt := hex.EncodeToString(m.TLS)
	var key strings.Builder
	key.WriteString("-----BEGIN OpenVPN Static key V1-----\n")
	for i := 0; i < len(tlsCrypt); i += 32 {
		key.WriteString(tlsCrypt[i:min(i+32, len(tlsCrypt))] + "\n")
	}
	key.WriteString("-----END OpenVPN Static key V1-----\n")

	const iface = "ovpnc0"
	profile := fmt.Sprintf(`client
dev %s
dev-type tun
proto %s
remote %s %d
nobind
remote-cert-tls server
tls-version-min 1.2
auth SHA256
data-ciphers AES-256-GCM:AES-128-GCM
route-nopull
verb 3
<ca>
%s</ca>
<cert>
%s</cert>
<key>
%s</key>
<tls-crypt>
%s</tls-crypt>
`, iface, proto, hostSide, m.Port,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: m.CA}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.Cert}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p.Key}),
		key.String())

	conf := filepath.Join(dir, "client.ovpn")
	if err := os.WriteFile(conf, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	startClient(t, "ip", "netns", "exec", clientNS, "openvpn", "--config", conf)

	deadline := time.Now().Add(30 * time.Second)
	for exec.Command("ip", "-n", clientNS, "-4", "addr", "show", iface).Run() != nil ||
		!hasIPv4(t, iface) {
		if time.Now().After(deadline) {
			t.Fatal("the OpenVPN client did not get a tunnel address")
		}
		time.Sleep(500 * time.Millisecond)
	}

	return iface
}

func hasIPv4(t *testing.T, iface string) bool {
	out, err := exec.Command("ip", "-n", clientNS, "-4", "-o", "addr", "show", iface).Output()
	return err == nil && strings.Contains(string(out), "inet ")
}

// TestOpenVPNDaemon runs the OpenVPN server without a client, which needs no
// namespace of its own and so runs under the systemd unit too: the server
// must open its tunnel, drop to the proxy account, answer the node over its
// management socket, and leave nothing behind when stopped.
//
// Rules: [RT-1], [RT-7].
func TestOpenVPNDaemon(t *testing.T) {
	requireEnv(t)
	if _, err := exec.LookPath("openvpn"); err != nil {
		t.Skip("openvpn is not installed here")
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Skip("no tun device here")
	}

	home := t.TempDir()
	common.SetEgress(common.Egress{APIPort: apiPort})
	rt, err := common.PrepareRuntime(home, os.Geteuid(), true)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Proxy == nil {
		t.Fatalf("the %s account is missing", common.ProxyUserName)
	}
	common.SetRuntime(rt)

	svc := openvpnService(t, home)
	if err := svc.Init(home); err != nil {
		t.Fatal("Init:", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatal("Start:", err)
	}
	checkDropped(t, "openvpn", rt)

	if _, err := svc.Peers(); err != nil {
		t.Error("Peers over the management socket:", err)
	}
	if err := stopWatched(t, svc); err != nil {
		t.Error("Stop:", err)
	}
	if left := processes(t, "openvpn", rt.Dir); len(left) != 0 {
		t.Errorf("openvpn still running after Stop: %v", left)
	}
	noChainsLeft(t)
}
