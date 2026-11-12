// SPDX-License-Identifier: Apache-2.0

package openvpn

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
	"github.com/Feryx-Official/dvpnd/v9/services/common/daemontest"
	ovpntypes "github.com/Feryx-Official/dvpnd/v9/services/openvpn/types"
)

func stubBinary(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "openvpn")
	script := "#!/bin/sh\ntrap 'exit 0' TERM\nwhile :; do sleep 0.1; done\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	saved := binaryName
	binaryName = path
	t.Cleanup(func() { binaryName = saved })
}

func home(t *testing.T, proto string, ipv6 bool) (string, *ovpntypes.Config) {
	t.Helper()

	dir := t.TempDir()
	cfg := ovpntypes.NewConfig().WithDefaultValues()
	cfg.ListenPort = 1194
	cfg.Proto = proto
	cfg.EnableIPv6 = ipv6
	cfg.Uplink = "eth0"
	if err := cfg.SaveToPath(filepath.Join(dir, ovpntypes.ConfigFileName)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", dir)
	saved := kernelHasIPv6
	kernelHasIPv6 = func() bool { return true }
	t.Cleanup(func() { kernelHasIPv6 = saved })

	return dir, cfg
}

// Rules: [CT-3].
func TestInitRendersServerConfig(t *testing.T) {
	stubBinary(t)
	dir, _ := home(t, ovpntypes.ProtoUDP, true)

	s := NewOpenVPN()
	if err := s.Init(dir); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "openvpn_server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, want := range []string{
		"dev ovpn0\n", "dev-type tun\n", "proto udp\n", "port 1194\n",
		"server 10.9.0.0 255.255.255.0\n", "server-ipv6 fd86:ea04:1116::/112\n", "topology subnet\n",
		"ca " + filepath.Join(dir, "openvpn", "ca.crt") + "\n",
		"tls-crypt " + filepath.Join(dir, "openvpn", "tc.key") + "\n",
		"dh none\n", "tls-cipher TLS-ECDHE-ECDSA-WITH-AES-256-GCM-SHA384\n",
		"data-ciphers AES-256-GCM:AES-128-GCM\n", "remote-cert-tls client\n",
		"management " + filepath.Join(dir, "openvpn.sock") + " unix\n", "management-client-user root\n",
		"management-client-auth\n", "auth-user-pass-optional\n",
		"explicit-exit-notify 1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("server.conf lacks %q:\n%s", want, out)
		}
	}
	if s.ListenPort() != 1194 || s.info[2] != transportUDP || s.info[3] != 1 {
		t.Fatalf("info: %x", s.info)
	}
	if s.nat.Interface != "ovpn0" || s.nat.Uplink != "eth0" {
		t.Fatalf("nat: %+v", s.nat)
	}

	dir, _ = home(t, ovpntypes.ProtoTCP, false)
	s = NewOpenVPN()
	if err := s.Init(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "openvpn_server.conf"))
	out = string(raw)
	if !strings.Contains(out, "proto tcp-server\n") || strings.Contains(out, "server-ipv6") || strings.Contains(out, "explicit-exit-notify") {
		t.Fatalf("tcp config:\n%s", out)
	}
	if s.info[2] != transportTCP || s.info[3] != 0 {
		t.Fatalf("tcp info: %x", s.info)
	}
	if pub, _ := json.Marshal(s.PublicMetadata()); string(pub) != `[{"port":0,"protocol":"tcp","ca":null,"tls":null}]` {
		t.Fatalf("public metadata: %s", pub)
	}
}

// Rules: [RT-6].
func TestInitRequiresBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	saved := binaryName
	binaryName = "openvpn"
	t.Cleanup(func() { binaryName = saved })

	err := NewOpenVPN().Init(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), `"openvpn" binary is not on PATH`) {
		t.Fatalf("Init without the binary: %v", err)
	}
}

// Rules: [SL-1], [CT-4].
func TestPeersAndHandshake(t *testing.T) {
	stubBinary(t)
	dir, _ := home(t, ovpntypes.ProtoUDP, true)
	s := NewOpenVPN()
	if err := s.Init(dir); err != nil {
		t.Fatal(err)
	}

	data, err := s.ParsePeerRequest([]byte(`{"uuid":[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16]}`))
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(data)
	cn := "01020304-0506-0708-090a-0b0c0d0e0f10"

	result, err := s.AddPeer(data)
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasPeer(data) || s.PeerCount() != 1 || !s.admit(cn) || s.admit("stranger") {
		t.Fatal("registration")
	}

	payload, err := s.HandshakePayload(result)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(payload)
	var doc struct {
		Metadata []struct {
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
			CA       string `json:"ca"`
			TLS      string `json:"tls"`
		} `json:"metadata"`
		Cert string `json:"cert"`
		Key  string `json:"key"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Metadata) != 1 || doc.Metadata[0].Port != 1194 || doc.Metadata[0].Protocol != "udp" {
		t.Fatalf("payload: %s", out)
	}
	ca, _ := base64.StdEncoding.DecodeString(doc.Metadata[0].CA)
	tls, _ := base64.StdEncoding.DecodeString(doc.Metadata[0].TLS)
	certDER, _ := base64.StdEncoding.DecodeString(doc.Cert)
	keyDER, _ := base64.StdEncoding.DecodeString(doc.Key)
	if len(tls) != 256 {
		t.Fatalf("tls-crypt key: %d bytes", len(tls))
	}
	caCert, err := x509.ParseCertificate(ca)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("client certificate: %v", err)
	}
	if cert.Subject.CommonName != cn {
		t.Fatalf("CN: %s", cert.Subject.CommonName)
	}
	if _, err := x509.ParsePKCS8PrivateKey(keyDER); err != nil {
		t.Fatalf("client key: %v", err)
	}

	// Usage: a live connection plus what ended connections used.
	f := startFakeServer(t)
	f.clients = []clientStatus{{commonName: cn, received: 10, sent: 200, cid: "3"}}
	conn, err := dialManagement(f.socket(), 2*1e9)
	if err != nil {
		t.Fatal(err)
	}
	s.mgmt = newManagement(conn, s.admit, s.disconnected)
	t.Cleanup(func() { s.mgmt.Close() })

	s.disconnected(cn, "2", 5, 50)
	s.disconnected("stranger", "1", 1, 1)
	peers, err := s.Peers()
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].Key != key || peers[0].Upload != 15 || peers[0].Download != 250 {
		t.Fatalf("peers: %+v", peers)
	}

	if err := s.RemovePeer(data); err != nil {
		t.Fatal(err)
	}
	if s.HasPeer(data) || s.admit(cn) {
		t.Fatal("removal")
	}
	if cmds := strings.Join(f.received(), "\n"); !strings.Contains(cmds, "client-kill 3") {
		t.Fatalf("kill not sent: %s", cmds)
	}

	if _, err := s.AddPeer([]byte{1, 2}); err == nil {
		t.Fatal("short peer data accepted")
	}
	if _, err := s.HandshakePayload([]byte{0, 0, 0, 9, 1}); err == nil {
		t.Fatal("truncated result accepted")
	}
	if s.Name() != "openvpn" || s.Type() != 3 {
		t.Fatalf("identity: %s/%d", s.Name(), s.Type())
	}
}

// Rules: [EG-7], [RT-3].
func TestStartStop(t *testing.T) {
	stubBinary(t)
	dir, _ := home(t, ovpntypes.ProtoUDP, true)

	var rules []string
	saved, savedQuiet := common.RunCommand, common.RunQuiet
	common.RunCommand = func(name string, args ...string) error {
		rules = append(rules, name+" "+strings.Join(args, " "))
		if slices.Contains(args, "-C") { // no rule or jump in place yet
			return os.ErrNotExist
		}
		return nil
	}
	common.RunQuiet = common.RunCommand
	t.Cleanup(func() { common.RunCommand, common.RunQuiet = saved, savedQuiet })

	savedFwd := ensureForwarding
	ensureForwarding = func() error { return nil }
	t.Cleanup(func() { ensureForwarding = savedFwd })

	s := NewOpenVPN()
	if err := s.Init(dir); err != nil {
		t.Fatal(err)
	}

	// The fake management server must listen where the config says.
	f := startFakeServerOn(t, s.socketPath)
	_ = f

	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if s.mgmt == nil {
		t.Fatal("not attached to the management interface")
	}
	joined := strings.Join(rules, "\n")
	if !strings.Contains(joined, "iptables -A FORWARD -i ovpn0 -j ACCEPT") ||
		!strings.Contains(joined, "iptables -t nat -A POSTROUTING -s 10.9.0.0/24 -o eth0 -j MASQUERADE") ||
		!strings.Contains(joined, "ip6tables -A FORWARD -o ovpn0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT") {
		t.Fatalf("rules:\n%s", joined)
	}
	// The egress chains go in first and are jumped to ahead of the accept rules.
	for _, rule := range []string{
		"iptables -A DVPND-FWD-ovpn0 -d 10.0.0.0/8 -j DROP",
		"iptables -A DVPND-FWD-ovpn0 -p tcp --dport 25 -j DROP",
		"iptables -I FORWARD 1 -i ovpn0 -j DVPND-FWD-ovpn0",
		"iptables -I INPUT 1 -i ovpn0 -j DVPND-IN-ovpn0",
	} {
		if !strings.Contains(joined, rule) {
			t.Fatalf("rules lack %q:\n%s", rule, joined)
		}
	}
	if strings.Index(joined, "DVPND-FWD-ovpn0") > strings.Index(joined, "-A FORWARD -i ovpn0 -j ACCEPT") {
		t.Fatalf("egress chains installed after the accept rules:\n%s", joined)
	}

	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(rules, "\n")
	if !strings.Contains(joined, "iptables -D FORWARD -i ovpn0 -j ACCEPT") ||
		!strings.Contains(joined, "iptables -D FORWARD -i ovpn0 -j DVPND-FWD-ovpn0") ||
		!strings.Contains(joined, "iptables -X DVPND-IN-ovpn0") {
		t.Fatalf("rules not removed:\n%s", joined)
	}
	if exited, _ := s.process.Exited(); !exited {
		t.Fatal("child not reaped")
	}
	if err := NewOpenVPN().Stop(); err == nil {
		t.Fatal("Stop before Start must fail")
	}
}

// TestDoneWhenTheServerOrTheManagementChannelEnds: the server dying, or the
// management channel closing (nothing is admitted or counted without it),
// closes Done, on which the start command stops the node. OpenVPN's AddPeer
// never reaches the server, so without it the node kept admitting paying
// clients.
//
// Rules: [RT-15].
func TestDoneWhenTheServerOrTheManagementChannelEnds(t *testing.T) {
	start := func(t *testing.T) (*OpenVPN, *fakeServer, string) {
		pidFile := filepath.Join(t.TempDir(), "pid")
		stubBinary(t)
		if err := os.WriteFile(binaryName, []byte(daemontest.Script(pidFile)), 0o755); err != nil {
			t.Fatal(err)
		}
		dir, _ := home(t, ovpntypes.ProtoUDP, false)

		saved, savedQuiet := common.RunCommand, common.RunQuiet
		common.RunCommand = func(_ string, args ...string) error {
			if slices.Contains(args, "-C") { // no rule or jump in place yet
				return os.ErrNotExist
			}
			return nil
		}
		common.RunQuiet = common.RunCommand
		t.Cleanup(func() { common.RunCommand, common.RunQuiet = saved, savedQuiet })
		savedFwd := ensureForwarding
		ensureForwarding = func() error { return nil }
		t.Cleanup(func() { ensureForwarding = savedFwd })

		s := NewOpenVPN()
		if err := s.Init(dir); err != nil {
			t.Fatal(err)
		}
		f := startFakeServerOn(t, s.socketPath)
		if err := s.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Stop() })

		return s, f, pidFile
	}

	t.Run("the server dies", func(t *testing.T) {
		s, _, pidFile := start(t)
		daemontest.Crash(t, pidFile, s.Done())
	})
	t.Run("the management channel closes", func(t *testing.T) {
		s, f, _ := start(t)
		<-f.ready
		f.mu.Lock()
		_ = f.conn.Close()
		f.mu.Unlock()
		daemontest.Closes(t, s.Done(), "the management channel closed")
	})
}

// TestVerbFollowsNodeLogLevel: OpenVPN logs every client's address at its
// usual verb 3, so the node asks for that only when it runs at debug itself.
//
// Rules: [PV-2].
func TestVerbFollowsNodeLogLevel(t *testing.T) {
	stubBinary(t)
	t.Cleanup(func() { common.SetVerbose(false) })

	for _, tc := range []struct {
		verbose bool
		want    string
	}{
		{verbose: false, want: "verb 0\n"},
		{verbose: true, want: "verb 3\n"},
	} {
		common.SetVerbose(tc.verbose)
		dir, _ := home(t, ovpntypes.ProtoUDP, false)
		if err := NewOpenVPN().Init(dir); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "openvpn_server.conf"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), tc.want) {
			t.Errorf("verbose %v: server.conf lacks %q:\n%s", tc.verbose, tc.want, raw)
		}
	}
}

func TestTunnelIPv4(t *testing.T) {
	if got := NewOpenVPN().TunnelIPv4().String(); got != "10.9.0.1" {
		t.Fatalf("tunnel IPv4: %s", got)
	}
}

// startOpenVPN starts the service on the stub binary, attached to a fake
// management server, with the firewall calls faked.
func startOpenVPN(t *testing.T) (*OpenVPN, *fakeServer) {
	t.Helper()

	stubBinary(t)
	dir, _ := home(t, ovpntypes.ProtoUDP, false)

	saved, savedQuiet := common.RunCommand, common.RunQuiet
	common.RunCommand = func(_ string, args ...string) error {
		if slices.Contains(args, "-C") { // no rule or jump in place yet
			return os.ErrNotExist
		}
		return nil
	}
	common.RunQuiet = common.RunCommand
	t.Cleanup(func() { common.RunCommand, common.RunQuiet = saved, savedQuiet })
	savedFwd := ensureForwarding
	ensureForwarding = func() error { return nil }
	t.Cleanup(func() { ensureForwarding = savedFwd })

	s := NewOpenVPN()
	if err := s.Init(dir); err != nil {
		t.Fatal(err)
	}
	f := startFakeServerOn(t, s.socketPath)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}

	return s, f
}

// TestStopWhileThePeersAreRead: the jobs read the usage and remove peers
// while the node stops; Stop drops the management connection under the
// lock those calls take it under, so none of them finds it half gone.
func TestStopWhileThePeersAreRead(t *testing.T) {
	s, f := startOpenVPN(t)
	data := []byte("0123456789abcdef")
	if _, err := s.AddPeer(data); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.clients = []clientStatus{{commonName: "30313233-3435-3637-3839-616263646566", received: 1, sent: 2, cid: "4"}}
	f.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_, _ = s.Peers()
			_ = s.RemovePeer(data)
		}
	}()
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	<-done
}

// TestAConnectionThatEndsDuringAPassCountsOnce: the usage pass reads the
// status table, then sums under the lock; a connection that ends between
// the two has its final bytes banked and is still in the table read before.
// It counts once.
//
// Rules: [SL-2].
func TestAConnectionThatEndsDuringAPassCountsOnce(t *testing.T) {
	s, f := startOpenVPN(t)
	t.Cleanup(func() { _ = s.Stop() })
	data := []byte("0123456789abcdef")
	cn := "30313233-3435-3637-3839-616263646566"
	if _, err := s.AddPeer(data); err != nil {
		t.Fatal(err)
	}

	// The table the pass read still lists connection 4, which has just
	// ended with 100 bytes up and 2000 down.
	f.mu.Lock()
	f.clients = []clientStatus{{commonName: cn, received: 90, sent: 1900, cid: "4"}}
	f.mu.Unlock()
	s.disconnected(cn, "4", 100, 2000)

	peers, err := s.Peers()
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].Upload != 100 || peers[0].Download != 2000 {
		t.Fatalf("want 100 up and 2000 down, got %+v", peers)
	}

	// The client is back on connection 5; the table no longer lists 4.
	f.mu.Lock()
	f.clients = []clientStatus{{commonName: cn, received: 1, sent: 2, cid: "5"}}
	f.mu.Unlock()
	if peers, err = s.Peers(); err != nil || peers[0].Upload != 101 || peers[0].Download != 2002 {
		t.Fatalf("want 101 up and 2002 down, got %+v, %v", peers, err)
	}
}

// TestAFailedKillKeepsThePeerCounted: a removal whose kill fails denies the
// peer's next connection at once, but keeps it registered and its open
// connection counted until a later removal kills it.
//
// Rules: [SL-16].
func TestAFailedKillKeepsThePeerCounted(t *testing.T) {
	s, f := startOpenVPN(t)
	t.Cleanup(func() { _ = s.Stop() })
	data := []byte("0123456789abcdef")
	cn := "30313233-3435-3637-3839-616263646566"
	if _, err := s.AddPeer(data); err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	f.clients = []clientStatus{{commonName: cn, received: 10, sent: 20, cid: "4"}}
	f.failKill = true
	f.mu.Unlock()
	if err := s.RemovePeer(data); err == nil {
		t.Fatal("a failed kill reported success")
	}
	if !s.HasPeer(data) || s.PeerCount() != 1 {
		t.Fatal("the peer was forgotten before its kill succeeded")
	}
	if _, err := s.AddPeer(data); err == nil {
		t.Fatal("the peer was added again before its removal finished")
	}
	if s.admit(cn) {
		t.Fatal("a removed peer may connect again")
	}
	peers, err := s.Peers()
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].Upload != 10 || peers[0].Download != 20 {
		t.Fatalf("want the open connection counted, got %+v", peers)
	}

	f.mu.Lock()
	f.failKill = false
	f.mu.Unlock()
	if err := s.RemovePeer(data); err != nil {
		t.Fatal(err)
	}
	if s.HasPeer(data) || s.PeerCount() != 0 || s.admit(cn) {
		t.Fatal("not removed once the kill worked")
	}
}

// TestUnreadableCountersAreNotZero: a byte count OpenVPN reports that cannot
// be read fails the status read, so the pass is tried again, and a final
// count that cannot be read banks what the status table last showed for the
// connection: a zero would make the peer's total drop.
//
// Rules: [SL-2].
func TestUnreadableCountersAreNotZero(t *testing.T) {
	s, f := startOpenVPN(t)
	t.Cleanup(func() { _ = s.Stop() })
	data := []byte("0123456789abcdef")
	cn := "30313233-3435-3637-3839-616263646566"
	if _, err := s.AddPeer(data); err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	f.clients = []clientStatus{{commonName: cn, received: 10, sent: 20, cid: "4"}}
	f.mu.Unlock()
	if peers, err := s.Peers(); err != nil || peers[0].Upload != 10 || peers[0].Download != 20 {
		t.Fatalf("want 10 up and 20 down, got %+v, %v", peers, err)
	}

	f.mu.Lock()
	f.clients = []clientStatus{{commonName: cn, received: -1, sent: 30, cid: "4"}}
	f.mu.Unlock()
	if peers, err := s.Peers(); err == nil {
		t.Fatalf("a status row with an unreadable count was read: %+v", peers)
	}

	f.mu.Lock()
	f.clients = nil
	f.mu.Unlock()
	f.event("DISCONNECT", "4", "", map[string]string{"common_name": cn, "bytes_received": "x", "bytes_sent": "25"})
	waitFor(t, "the disconnect", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		_, ok := s.banked["4"]
		return ok
	})
	if peers, err := s.Peers(); err != nil || peers[0].Upload != 10 || peers[0].Download != 25 {
		t.Fatalf("want 10 up and 25 down, got %+v, %v", peers, err)
	}
}

// TestNATRulesAreScopedAndAddedOnce: the masquerade rule matches the
// tunnel's own network, and a rule a node that died without Stop left
// behind is not added a second time.
//
// Rules: [EG-9], [EG-14].
func TestNATRulesAreScopedAndAddedOnce(t *testing.T) {
	for _, left := range []bool{false, true} {
		stubBinary(t)
		dir, _ := home(t, ovpntypes.ProtoUDP, false)

		var rules []string
		saved, savedQuiet := common.RunCommand, common.RunQuiet
		common.RunCommand = func(name string, args ...string) error {
			rules = append(rules, name+" "+strings.Join(args, " "))
			if slices.Contains(args, "-C") && !left {
				return os.ErrNotExist
			}
			return nil
		}
		common.RunQuiet = common.RunCommand
		t.Cleanup(func() { common.RunCommand, common.RunQuiet = saved, savedQuiet })
		savedFwd := ensureForwarding
		ensureForwarding = func() error { return nil }
		t.Cleanup(func() { ensureForwarding = savedFwd })

		s := NewOpenVPN()
		if err := s.Init(dir); err != nil {
			t.Fatal(err)
		}
		startFakeServerOn(t, s.socketPath)
		if err := s.Start(); err != nil {
			t.Fatal(err)
		}
		_ = s.Stop()

		joined := strings.Join(rules, "\n")
		if left {
			for _, rule := range rules {
				if strings.Contains(rule, "-A FORWARD -i ovpn0 -j ACCEPT") || strings.Contains(rule, "-A POSTROUTING") {
					t.Errorf("a rule left behind was added again: %s", rule)
				}
			}
			continue
		}
		if !strings.Contains(joined, "iptables -t nat -A POSTROUTING -s 10.9.0.0/24 -o eth0 -j MASQUERADE") {
			t.Errorf("the masquerade rule is not scoped to the tunnel:\n%s", joined)
		}
	}
}

// TestIPv6OffMeansNoIPv6: with enable_ipv6 = false, or on a kernel booted
// without IPv6, the server offers no IPv6 and the node calls no ip6tables
// for its forwarding and NAT rules.
//
// Rules: [EG-11].
func TestIPv6OffMeansNoIPv6(t *testing.T) {
	for _, tc := range []struct {
		name         string
		setting, has bool
	}{
		{"setting off", false, true},
		{"kernel without IPv6", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubBinary(t)
			dir, _ := home(t, ovpntypes.ProtoUDP, tc.setting)
			savedKernel := kernelHasIPv6
			kernelHasIPv6 = func() bool { return tc.has }
			t.Cleanup(func() { kernelHasIPv6 = savedKernel })

			var rules []string
			saved, savedQuiet := common.RunCommand, common.RunQuiet
			common.RunCommand = func(name string, args ...string) error {
				rules = append(rules, name+" "+strings.Join(args, " "))
				if slices.Contains(args, "-C") {
					return os.ErrNotExist
				}
				return nil
			}
			common.RunQuiet = common.RunCommand
			t.Cleanup(func() { common.RunCommand, common.RunQuiet = saved, savedQuiet })
			savedFwd := ensureForwarding
			ensureForwarding = func() error { return nil }
			t.Cleanup(func() { ensureForwarding = savedFwd })

			s := NewOpenVPN()
			if err := s.Init(dir); err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(filepath.Join(dir, "openvpn_server.conf"))
			if strings.Contains(string(raw), "server-ipv6") || s.info[3] != 0 {
				t.Errorf("IPv6 offered: info %x\n%s", s.info, raw)
			}
			startFakeServerOn(t, s.socketPath)
			if err := s.Start(); err != nil {
				t.Fatal(err)
			}
			_ = s.Stop()
			for _, rule := range rules {
				if strings.HasPrefix(rule, "ip6tables") && !strings.Contains(rule, "DVPND-") {
					t.Errorf("an ip6tables forwarding or NAT rule: %s", rule)
				}
			}
		})
	}
}
