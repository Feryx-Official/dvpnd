// SPDX-License-Identifier: Apache-2.0

package common

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Rules: [EG-6].
func TestProxyEgress(t *testing.T) {
	f := fakeIptables(t)
	withEgress(t, Egress{APIPort: 8585})

	saved := resolvConf
	resolvConf = filepath.Join(t.TempDir(), "resolv.conf")
	t.Cleanup(func() { resolvConf = saved })
	conf := "# generated\nnameserver 127.0.0.53\nnameserver fe80::1%eth0\noptions edns0\nsearch example\n"
	if err := os.WriteFile(resolvConf, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}

	e := ProxyEgress{UID: 997, LoopbackPorts: []uint16{41234}}
	if err := e.Up(); err != nil {
		t.Fatal(err)
	}

	for _, tool := range []string{"iptables", "ip6tables"} {
		if got := f.chains[tool]["OUTPUT"]; len(got) != 1 || got[0] != "-m owner --uid-owner 997 -j DVPND-PROXY" {
			t.Fatalf("%s OUTPUT: %v", tool, got)
		}

		want := []string{"-m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT"}
		blocked := BlockedNetworksV6
		if tool == "iptables" {
			blocked = BlockedNetworksV4
			want = append(want,
				"-o lo -d 127.0.0.1 -p tcp --dport 41234 -j ACCEPT",
				"-p udp -m conntrack --ctorigdst 127.0.0.53 --ctorigdstport 53 -j ACCEPT",
				"-p tcp -m conntrack --ctorigdst 127.0.0.53 --ctorigdstport 53 -j ACCEPT")
		} else {
			want = append(want,
				"-p udp -m conntrack --ctorigdst fe80::1 --ctorigdstport 53 -j ACCEPT",
				"-p tcp -m conntrack --ctorigdst fe80::1 --ctorigdstport 53 -j ACCEPT")
		}
		for _, cidr := range blocked {
			want = append(want, "-d "+cidr+" -j REJECT")
		}
		// The host's own addresses, after the blocked networks (loopback
		// among them): the API port, nothing else.
		want = append(want,
			"-m addrtype --dst-type LOCAL -p tcp --dport 8585 -j ACCEPT",
			"-m addrtype --dst-type LOCAL -j REJECT")
		want = append(want, "-p tcp --dport 25 -j REJECT")
		if got := f.chains[tool][ProxyChain]; !slices.Equal(got, want) {
			t.Fatalf("%s chain:\n%s\nwant:\n%s", tool, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}

	e.Down()
	for _, tool := range []string{"iptables", "ip6tables"} {
		if _, ok := f.chains[tool][ProxyChain]; ok || len(f.chains[tool]["OUTPUT"]) != 0 {
			t.Fatalf("%s after Down: %v", tool, f.chains[tool])
		}
	}
}

// Rules: [RT-1].
func TestPrepareRuntimeAsAUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNTIME_DIRECTORY", "")

	rt, err := PrepareRuntime(home, 1000, true)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Dir != filepath.Join(home, "run") || rt.Proxy != nil {
		t.Fatalf("runtime %+v", rt)
	}
	if info, err := os.Stat(rt.Dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory: %v %v", info.Mode(), err)
	}

	path, err := rt.WriteFile("x.json", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %o", info.Mode().Perm())
	}
	if err := os.WriteFile(filepath.Join(home, "tls.crt"), []byte("cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "tls.key"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, key, err := rt.TLSFiles(home)
	if err != nil || filepath.Dir(cert) != rt.Dir || filepath.Dir(key) != rt.Dir {
		t.Fatalf("tls copies %s %s: %v", cert, key, err)
	}

	// Under systemd the unit's RuntimeDirectory wins.
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(home, "systemd")+":/other")
	if rt, err = PrepareRuntime(home, 1000, true); err != nil || rt.Dir != filepath.Join(home, "systemd") {
		t.Fatalf("runtime %+v: %v", rt, err)
	}
}

// TestPrepareRuntimeAsRoot runs only as root, which may hand the directory
// to the proxy account's group.
//
// Rules: [RT-1].
func TestPrepareRuntimeAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	saved := lookupProxyUser
	lookupProxyUser = func() (*ProxyUser, error) {
		return &ProxyUser{Name: "nobody", Group: "nogroup", UID: 65534, GID: 65534}, nil
	}
	t.Cleanup(func() { lookupProxyUser = saved })
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))

	rt, err := PrepareRuntime(t.TempDir(), 0, true)
	if err != nil || rt.Proxy == nil {
		t.Fatalf("runtime %+v: %v", rt, err)
	}
	path, err := rt.WriteFile("x.json", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Fatalf("file mode %o", info.Mode().Perm())
	}
}

// Rules: [RT-1].
func TestProxyAttr(t *testing.T) {
	if (Runtime{}).proxyAttr(443) != nil {
		t.Fatal("without a proxy account the daemon runs as the node")
	}

	rt := Runtime{Proxy: &ProxyUser{UID: 997, GID: 996}}
	attr := rt.proxyAttr(8443)
	if attr.Credential.Uid != 997 || attr.Credential.Gid != 996 || len(attr.Credential.Groups) != 0 ||
		len(attr.AmbientCaps) != 0 {
		t.Fatalf("attr %+v", attr)
	}
	if attr = rt.proxyAttr(443); len(attr.AmbientCaps) != 1 || attr.AmbientCaps[0] != capNetBindService {
		t.Fatalf("a port below 1024 needs CAP_NET_BIND_SERVICE: %+v", attr)
	}
}

// TestRemoveFilesTakesWhatTheDaemonsRead: the files the node wrote for its
// daemons (a configuration holding a REALITY private key or the stats secret,
// the copy of the TLS key) go when the node stops, and nothing else in the
// directory does.
//
// Rules: [PV-10].
func TestRemoveFilesTakesWhatTheDaemonsRead(t *testing.T) {
	rt := Runtime{Dir: t.TempDir()}
	home := t.TempDir()
	for _, name := range []string{"tls.crt", "tls.key"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config, err := rt.WriteFile("xray_config.json", []byte(`{"privateKey":"not for the disk"}`))
	if err != nil {
		t.Fatal(err)
	}
	cert, key, err := rt.TLSFiles(home)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(rt.Dir, "not-ours")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := rt.RemoveFiles(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{config, cert, key} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s is still there after RemoveFiles: %v", filepath.Base(path), err)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("RemoveFiles took a file the node did not write: %v", err)
	}
	if err := rt.RemoveFiles(); err != nil {
		t.Errorf("a second RemoveFiles: %v", err)
	}
}
