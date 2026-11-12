// SPDX-License-Identifier: Apache-2.0

package amneziawg

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	awgtypes "github.com/Feryx-Official/dvpnd/v9/services/amneziawg/types"
	wgtypes "github.com/Feryx-Official/dvpnd/v9/services/wireguard/types"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// fakeAwg stands in for the awg tool on PATH: it records every call and
// keeps a peer list per interface, which "show <iface> transfer" prints with
// fixed counters.
const fakeAwg = `#!/bin/sh
dir="$(dirname "$0")"
echo "$@" >> "$dir/calls.log"
case "$1" in
  set)
    if [ "$5" = remove ]; then
      grep -vF "$4" "$dir/$2.peers" > "$dir/$2.tmp" 2>/dev/null
      mv "$dir/$2.tmp" "$dir/$2.peers"
    else
      printf '%s\t%d\t%d\n' "$4" 100 200 >> "$dir/$2.peers"
    fi ;;
  show)
    cat "$dir/$2.peers" 2>/dev/null ;;
esac
exit 0
`

func setup(t *testing.T) (string, *awgtypes.Config, *AmneziaWG) {
	t.Helper()

	saved := lookPath
	lookPath = func(string) (string, error) { return "/usr/bin/awg", nil }
	t.Cleanup(func() { lookPath = saved })

	confDir := t.TempDir()
	savedDir := Variant.ConfigDir
	Variant.ConfigDir = confDir
	t.Cleanup(func() { Variant.ConfigDir = savedDir })

	cfg := awgtypes.NewConfig().WithDefaultValues()
	cfg.ListenPort = 51821
	cfg.V3.ListenPort = 51822
	cfg.Uplink = "eth0"
	cfg.Obfuscation.I1 = "<b 0xabcd><r 8>"

	return confDir, cfg, newService(t)
}

// newService builds the service over fixed pools, so payloads in the tests
// carry known addresses.
func newService(t *testing.T) *AmneziaWG {
	t.Helper()

	pool, err := newPool("10.8.0.2/24", "fd86:ea04:1115::2/120")
	if err != nil {
		t.Fatal(err)
	}
	poolV3, err := newPool("10.9.0.2/24", "fd86:ea04:1116::2/120")
	if err != nil {
		t.Fatal(err)
	}

	return NewAmneziaWG(pool, poolV3)
}

func newPool(v4, v6 string) (*wgtypes.IPPool, error) {
	ipv4Pool, err := wgtypes.NewIPv4PoolFromCIDR(v4)
	if err != nil {
		return nil, err
	}

	ipv6Pool, err := wgtypes.NewIPv6PoolFromCIDR(v6)
	if err != nil {
		return nil, err
	}

	return wgtypes.NewIPPool(ipv4Pool, ipv6Pool), nil
}

// home writes cfg as amneziawg.toml under a fresh directory and returns it.
func home(t *testing.T, cfg *awgtypes.Config) string {
	t.Helper()

	dir := t.TempDir()
	if err := cfg.SaveToPath(filepath.Join(dir, awgtypes.ConfigFileName)); err != nil {
		t.Fatal(err)
	}

	return dir
}

// withFakeAwg puts the stand-in tool first on PATH and returns its directory.
func withFakeAwg(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "awg"), []byte(fakeAwg), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(raw)
}

func newKey(t *testing.T) *wgtypes.Key {
	t.Helper()

	key, err := wgtypes.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}

	return key.Public()
}

// Rules: [CT-7].
func TestInitWritesInterfaceConfigs(t *testing.T) {
	confDir, cfg, s := setup(t)

	if err := s.Init(t.TempDir()); err == nil {
		t.Fatal("Init without amneziawg.toml must fail")
	}
	if err := s.Init(home(t, cfg)); err != nil {
		t.Fatal(err)
	}

	out := readFile(t, filepath.Join(confDir, "awg0.conf"))
	o := cfg.Obfuscation
	for _, want := range []string{
		"Address = 10.8.0.1/24,fd86:ea04:1115::1/120",
		"ListenPort = 51821",
		"PrivateKey = " + cfg.PrivateKey,
		"Jc = 4", "Jmin = 40", "Jmax = 70",
		"S1 = " + itoa(uint64(o.S1)), "S2 = " + itoa(uint64(o.S2)), "S3 = 0", "S4 = 0",
		"H1 = " + itoa(uint64(o.H1)), "H4 = " + itoa(uint64(o.H4)),
		"I1 = <b 0xabcd><r 8>",
		"-o eth0 -j MASQUERADE",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("awg0.conf lacks %q:\n%s", want, out)
		}
	}
	for _, forbidden := range []string{"I2 =", "HeaderProtectionKey", "RandomTrailers", "ContentPaddingAddition", "MTU ="} {
		if strings.Contains(out, forbidden) {
			t.Errorf("the default tier must not carry %q:\n%s", forbidden, out)
		}
	}

	v := cfg.V3
	out = readFile(t, filepath.Join(confDir, "awg1.conf"))
	for _, want := range []string{
		"Address = 10.9.0.1/24,fd86:ea04:1116::1/120",
		"ListenPort = 51822",
		"PrivateKey = " + v.PrivateKey,
		"MTU = 1280",
		"Jc = 4", "Jmin = 40", "Jmax = 70",
		"S1 = " + itoa(uint64(v.S1)), "S3 = " + itoa(uint64(v.S3)), "S4 = " + itoa(uint64(v.S4)),
		"H1 = " + itoa(uint64(v.H1)), "H4 = " + itoa(uint64(v.H4)),
		"HeaderProtectionKey = " + v.HeaderProtectionKey,
		"RandomTrailers = on",
		"ContentPaddingAddition = 0-64",
		"I1 = <b 0xabcd><r 8>",
		"-o eth0 -j MASQUERADE",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("awg1.conf lacks %q:\n%s", want, out)
		}
	}

	if s.Name() != "amneziawg" || s.Type() != 5 || s.ListenPort() != 51821 {
		t.Fatalf("identity: %s/%d port %d", s.Name(), s.Type(), s.ListenPort())
	}
	if s.Config().Interface != "awg0" || s.v3.Config().Interface != "awg1" {
		t.Fatalf("interfaces: %s, %s", s.Config().Interface, s.v3.Config().Interface)
	}
}

// A node's service has no preset pools: each tier runs on the networks its
// key derives, and a peer's address still says which tier it is on.
//
// Rules: [CT-8].
func TestInitDerivesTunnelNetworks(t *testing.T) {
	withFakeAwg(t)
	confDir, cfg, _ := setup(t)
	s := NewAmneziaWG(nil, nil)
	if err := s.Init(home(t, cfg)); err != nil {
		t.Fatal(err)
	}

	def, v3, err := cfg.TunnelNetworks()
	if err != nil {
		t.Fatal(err)
	}
	host := func(n *net.IPNet, step int) string {
		if v4 := n.IP.To4(); v4 != nil {
			ip := wgtypes.NewIPv4FromIP(v4)
			for i := 0; i < step; i++ {
				ip = ip.Next()
			}
			return ip.IP().String()
		}
		ip := wgtypes.NewIPv6FromIP(n.IP)
		for i := 0; i < step; i++ {
			ip = ip.Next()
		}
		return ip.IP().String()
	}

	for file, n := range map[string]awgtypes.Networks{"awg0.conf": def, "awg1.conf": v3} {
		want := "Address = " + host(n.IPv4, 1) + "/24," + host(n.IPv6, 1) + "/120"
		if out := readFile(t, filepath.Join(confDir, file)); !strings.Contains(out, want) {
			t.Errorf("%s lacks %q:\n%s", file, want, out)
		}
		if strings.HasPrefix(n.IPv4.String(), "10.8.") || strings.HasPrefix(n.IPv4.String(), "10.9.") {
			t.Errorf("%s runs on %s", file, n.IPv4)
		}
	}
	// The Handshake resolver listens on the default tier's interface address;
	// clients of the 3.1 tier reach it there too, as it is the node's own.
	if got, want := s.TunnelIPv4().String(), host(def.IPv4, 1); got != want {
		t.Errorf("tunnel IPv4: %s, want %s", got, want)
	}

	for _, tc := range []struct {
		req  string
		net  awgtypes.Networks
		tier string
	}{
		{`{"public_key":"%s"}`, def, "default"},
		{`{"public_key":"%s","awg_version":3}`, v3, "3.1"},
	} {
		key := newKey(t)
		req := []byte(fmt.Sprintf(tc.req, key.String()))
		data, err := s.ParsePeerRequest(req)
		if err != nil {
			t.Fatal(err)
		}
		res, err := types.AddPeer(s, req, data)
		if err != nil {
			t.Fatal(err)
		}
		if got := net.IP(res[:4]).String(); got != host(tc.net.IPv4, 2) {
			t.Fatalf("%s tier peer got %s, want %s", tc.tier, got, host(tc.net.IPv4, 2))
		}
		payload, err := s.HandshakePayload(res)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(payload)
		if strings.Contains(string(out), `"awg_version":3`) != (tc.tier == "3.1") {
			t.Fatalf("%s tier payload: %s", tc.tier, out)
		}
	}
}

// Rules: [CT-3], [CT-4], [CT-7].
func TestHandshakePayload(t *testing.T) {
	_, cfg, s := setup(t)
	if err := s.Init(home(t, cfg)); err != nil {
		t.Fatal(err)
	}

	o, v := cfg.Obfuscation, cfg.V3

	// The default tier's payload is what current apps have always received.
	v4 := []byte{10, 8, 0, 3}
	v6 := []byte{0xfd, 0x86, 0xea, 0x04, 0x11, 0x15, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3}
	want := `{"addrs":["10.8.0.3/32","fd86:ea04:1115::3/128"],"metadata":[{"port":51821,"public_key":"` + s.PublicKey() + `",` +
		`"s1":` + itoa(uint64(o.S1)) + `,"s2":` + itoa(uint64(o.S2)) + `,"s3":0,"s4":0,` +
		`"h1":` + itoa(uint64(o.H1)) + `,"h2":` + itoa(uint64(o.H2)) + `,"h3":` + itoa(uint64(o.H3)) + `,"h4":` + itoa(uint64(o.H4)) + `,` +
		`"i1":"<b 0xabcd><r 8>"}]}`
	assertPayload(t, s, append(append([]byte{}, v4...), v6...), want)

	// A peer on the 3.1 tier's subnet gets that tier's entry.
	v4 = []byte{10, 9, 0, 3}
	v6 = []byte{0xfd, 0x86, 0xea, 0x04, 0x11, 0x16, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3}
	want = `{"addrs":["10.9.0.3/32","fd86:ea04:1116::3/128"],"metadata":[{"port":51822,"public_key":"` + s.v3.PublicKey() + `",` +
		`"s1":` + itoa(uint64(v.S1)) + `,"s2":` + itoa(uint64(v.S2)) + `,"s3":` + itoa(uint64(v.S3)) + `,"s4":` + itoa(uint64(v.S4)) + `,` +
		`"h1":` + itoa(uint64(v.H1)) + `,"h2":` + itoa(uint64(v.H2)) + `,"h3":` + itoa(uint64(v.H3)) + `,"h4":` + itoa(uint64(v.H4)) + `,` +
		`"i1":"<b 0xabcd><r 8>","awg_version":3,"header_protection_key":"` + v.HeaderProtectionKey + `","random_trailers":true,"mtu":1280}]}`
	assertPayload(t, s, append(append([]byte{}, v4...), v6...), want)

	pub, _ := json.Marshal(s.PublicMetadata())
	blank := `"port":0,"public_key":null,"s1":0,"s2":0,"s3":0,"s4":0,"h1":0,"h2":0,"h3":0,"h4":0`
	if string(pub) != `[{`+blank+`,"awg_version":2},{`+blank+`,"awg_version":3}]` {
		t.Fatalf("public metadata: %s", pub)
	}
	if _, err := s.HandshakePayload([]byte{1}); err == nil {
		t.Fatal("short result accepted")
	}
}

// assertPayload compares the rendered payload with want as decoded JSON:
// encoding/json escapes < and > as \u003c and \u003e.
func assertPayload(t *testing.T, s *AmneziaWG, result []byte, want string) {
	t.Helper()

	payload, err := s.HandshakePayload(result)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(payload)

	var got, exp interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &exp); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, exp) {
		t.Fatalf("payload:\n got %s\nwant %s", out, want)
	}
}

// Rules: [CT-7], [SL-1].
func TestPeersGoToTheTierTheyAskedFor(t *testing.T) {
	tool := withFakeAwg(t)
	_, cfg, s := setup(t)
	if err := s.Init(home(t, cfg)); err != nil {
		t.Fatal(err)
	}

	// No awg_version: the default tier and its subnet.
	key := newKey(t)
	req := []byte(`{"public_key":"` + key.String() + `"}`)
	data, err := s.ParsePeerRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	res, err := types.AddPeer(s, req, data)
	if err != nil {
		t.Fatal(err)
	}
	if got := res[:4]; !reflect.DeepEqual(got, []byte{10, 8, 0, 2}) {
		t.Fatalf("default tier address: %v", got)
	}

	// awg_version 3: the second interface and its subnet, and the 3.1 entry.
	key3 := newKey(t)
	req3 := []byte(`{"public_key":"` + key3.String() + `","awg_version":3}`)
	data3, err := s.ParsePeerRequest(req3)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(data3, key3.Bytes()) {
		t.Fatal("the peer data must stay the 32-byte key")
	}
	res3, err := types.AddPeer(s, req3, data3)
	if err != nil {
		t.Fatal(err)
	}
	if got := res3[:4]; !reflect.DeepEqual(got, []byte{10, 9, 0, 2}) {
		t.Fatalf("3.1 tier address: %v", got)
	}
	payload, err := s.HandshakePayload(res3)
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := json.Marshal(payload); !strings.Contains(string(out), `"awg_version":3`) {
		t.Fatalf("3.1 tier payload: %s", out)
	}

	calls := readFile(t, filepath.Join(tool, "calls.log"))
	for _, want := range []string{
		"set awg0 peer " + key.String() + " allowed-ips 10.8.0.2/32,fd86:ea04:1115::2/128",
		"set awg1 peer " + key3.String() + " allowed-ips 10.9.0.2/32,fd86:ea04:1116::2/128",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("awg was not called with %q:\n%s", want, calls)
		}
	}

	// Usage comes from both interfaces under the session keys.
	peers, err := s.Peers()
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 2 || peers[0].Key != key.String() || peers[1].Key != key3.String() ||
		peers[1].Upload != 100 || peers[1].Download != 200 {
		t.Fatalf("peers: %+v", peers)
	}
	if s.PeerCount() != 2 || !s.HasPeer(data3) || !s.HasPeer(data) {
		t.Fatalf("count %d, has %v %v", s.PeerCount(), s.HasPeer(data), s.HasPeer(data3))
	}

	// Removal reaches the interface that has the peer.
	if err := s.RemovePeer(data3); err != nil {
		t.Fatal(err)
	}
	if calls = readFile(t, filepath.Join(tool, "calls.log")); !strings.Contains(calls, "set awg1 peer "+key3.String()+" remove") {
		t.Fatalf("removal:\n%s", calls)
	}
	if peers, _ = s.Peers(); len(peers) != 1 || s.PeerCount() != 1 || s.HasPeer(data3) {
		t.Fatalf("after removal: %+v, count %d", peers, s.PeerCount())
	}

	// A peer added without a request of its own lands on the default tier.
	other := newKey(t)
	if res, err = s.AddPeer(other.Bytes()); err != nil || res[0] != 10 || res[1] != 8 {
		t.Fatalf("unasked peer: %v %v", res, err)
	}
}

// Rules: [CT-5], [CT-7].
func TestParsePeerRequestVersions(t *testing.T) {
	_, cfg, s := setup(t)
	if err := s.Init(home(t, cfg)); err != nil {
		t.Fatal(err)
	}
	key := newKey(t).String()

	for _, tc := range []struct {
		raw string
		ok  bool
	}{
		{`{"public_key":"` + key + `"}`, true},
		{`{"public_key":"` + key + `","awg_version":2}`, true},
		{`{"public_key":"` + key + `","awg_version":3}`, true},
		{`{"public_key":"` + key + `","awg_version":4}`, false},
		{`{"public_key":"` + key + `","awg_version":"3"}`, false},
		{`{"uuid":"x"}`, false},
	} {
		if _, err := s.ParsePeerRequest([]byte(tc.raw)); (err == nil) != tc.ok {
			t.Errorf("%s: err %v", tc.raw, err)
		}
	}
}

// Rules: [CT-3], [CT-7].
func TestTierDisabled(t *testing.T) {
	confDir, cfg, s := setup(t)
	cfg.V3.Enabled = false
	if err := s.Init(home(t, cfg)); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(confDir, "awg1.conf")); err == nil {
		t.Fatal("a disabled tier must not get an interface configuration")
	}
	key := newKey(t).String()
	_, err := s.ParsePeerRequest([]byte(`{"public_key":"` + key + `","awg_version":3}`))
	if err == nil || !strings.Contains(err.Error(), "offers 2 only") {
		t.Fatalf("awg_version 3 on a default-only node: %v", err)
	}
	if pub, _ := json.Marshal(s.PublicMetadata()); string(pub) != `[{"port":0,"public_key":null,"s1":0,"s2":0,"s3":0,"s4":0,"h1":0,"h2":0,"h3":0,"h4":0,"awg_version":2}]` {
		t.Fatalf("public metadata: %s", pub)
	}

	// A peer on the 3.1 subnet cannot exist; its payload is the default one.
	v4 := []byte{10, 9, 0, 3}
	v6 := []byte{0xfd, 0x86, 0xea, 0x04, 0x11, 0x16, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3}
	payload, err := s.HandshakePayload(append(append([]byte{}, v4...), v6...))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := payload.(HandshakePayloadData); !ok {
		t.Fatalf("payload type %T", payload)
	}
}

// Rules: [RT-6].
func TestInitRequiresTools(t *testing.T) {
	_, cfg, s := setup(t)
	lookPath = func(name string) (string, error) { return "", errors.New("not found: " + name) }

	err := s.Init(home(t, cfg))
	if err == nil || !strings.Contains(err.Error(), `"awg" tool is not on PATH`) {
		t.Fatalf("Init without the tools: %v", err)
	}
}

func itoa(v uint64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestARequestLeavesNoTierBehind: the tier a request asks for travels with
// that request. A request that never reached admission, as when the chain
// refuses its session, leaves nothing behind: the same key added later
// without a request, as the legacy endpoint adds it, gets the default tier.
// Two requests in flight for one key each get the tier they asked for.
//
// Rules: [CT-7].
func TestARequestLeavesNoTierBehind(t *testing.T) {
	withFakeAwg(t)
	_, cfg, s := setup(t)
	if err := s.Init(home(t, cfg)); err != nil {
		t.Fatal(err)
	}

	key := newKey(t)
	if _, err := s.ParsePeerRequest([]byte(`{"public_key":"` + key.String() + `","awg_version":3}`)); err != nil {
		t.Fatal(err)
	}
	res, err := s.AddPeer(key.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got := res[:4]; !reflect.DeepEqual(got, []byte{10, 8, 0, 2}) {
		t.Fatalf("a peer added without asking for a tier got %v, not a default tier address", got)
	}

	other := newKey(t)
	v3 := []byte(`{"public_key":"` + other.String() + `","awg_version":3}`)
	for _, req := range [][]byte{v3, []byte(`{"public_key":"` + other.String() + `"}`)} {
		if _, err := s.ParsePeerRequest(req); err != nil {
			t.Fatal(err)
		}
	}
	if res, err = types.AddPeer(s, v3, other.Bytes()); err != nil {
		t.Fatal(err)
	}
	if got := res[:4]; !reflect.DeepEqual(got, []byte{10, 9, 0, 2}) {
		t.Fatalf("a 3.1 request parsed before a default one for the same key got %v", got)
	}
}
