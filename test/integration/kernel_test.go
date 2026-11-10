// SPDX-License-Identifier: Apache-2.0

//go:build integration

package integration

import (
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
)

// TestProxyAccountChain checks the kernel's hold on the proxy account on its
// own, with no proxy in between: whatever a daemon running as the account
// dials, the DVPND-PROXY chain must keep it to the internet and the node API.
// That is what stops a name that resolves to a public address when the
// daemon checks it and to a private one when it dials (DNS rebinding). The
// same dials first run without the chain, so the test cannot pass on a
// network where the blocked destinations are unreachable anyway.
//
// Rules: [EG-6].
func TestProxyAccountChain(t *testing.T) {
	requireEnv(t)

	common.SetEgress(common.Egress{APIPort: apiPort})
	rt, err := common.PrepareRuntime(t.TempDir(), os.Geteuid(), true)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Proxy == nil {
		t.Fatalf("the %s account is missing", common.ProxyUserName)
	}
	attr := &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: rt.Proxy.UID, Gid: rt.Proxy.GID, Groups: []uint32{}},
	}

	view := []reach{
		{net.JoinHostPort(env.public, strconv.Itoa(targetPort)), true},
		// A name: the account must still reach the host's resolver.
		{net.JoinHostPort(env.name, strconv.Itoa(targetPort)), true},
		{net.JoinHostPort(env.self, strconv.Itoa(apiPort)), true},
		{net.JoinHostPort(env.public, strconv.Itoa(common.SMTPPort)), false},
		{net.JoinHostPort(env.private, strconv.Itoa(targetPort)), false},
		{net.JoinHostPort(env.self, strconv.Itoa(hostPort)), false},
		{net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort)), false},
		// The API is open on the host's public address only.
		{net.JoinHostPort("127.0.0.1", strconv.Itoa(apiPort)), false},
		{net.JoinHostPort("rebind.test", strconv.Itoa(hostPort)), false},
	}

	// Control: without the chain the account reaches all of it.
	open := make([]reach, len(view))
	for i, c := range view {
		open[i] = reach{c.target, true}
	}
	checkReach(t, runDial(t, nil, attr, open), open)
	if t.Failed() {
		t.Fatal("the test network does not let the account reach every destination without the chain")
	}

	chain := common.ProxyEgress{UID: rt.Proxy.UID}
	if err := chain.Up(); err != nil {
		t.Fatal("Up:", err)
	}
	t.Cleanup(chain.Down)
	checkReach(t, runDial(t, nil, attr, view), view)

	// Root, which the node runs as, is not held by the account's chain.
	checkReach(t, runDial(t, nil, nil, open), open)

	chain.Down()
	noChainsLeft(t)
}

// TestTunnelEgressRules installs a tunnel's egress chains with the host's
// real iptables and ip6tables, which must take every rule, twice in a row
// (a node that died without removing them starts again), and leave nothing
// behind.
//
// Rules: [EG-9], [RT-7].
func TestTunnelEgressRules(t *testing.T) {
	requireEnv(t)

	// The rules match the interface by name; it need not exist.
	const iface = "dvit0"
	common.SetEgress(common.Egress{APIPort: apiPort, Resolver: net.ParseIP("10.8.0.1")})
	e := common.TunnelEgress{Interface: iface}
	for i := 0; i < 2; i++ {
		if err := e.Up(); err != nil {
			t.Fatal("Up:", err)
		}
	}
	t.Cleanup(e.Down)

	for _, f := range []struct {
		tool    string
		blocked []string
		dns     int
	}{
		{"iptables", common.BlockedNetworksV4, 2},
		{"ip6tables", common.BlockedNetworksV6, 0},
	} {
		if f.tool == "ip6tables" {
			if _, err := os.Stat("/proc/sys/net/ipv6"); err != nil {
				continue
			}
		}
		rules := iptablesRules(t, f.tool, "FORWARD")
		if n := strings.Count(rules, "-j "+e.ForwardChain()); n != 1 {
			t.Errorf("%s FORWARD jumps to %s %d times:\n%s", f.tool, e.ForwardChain(), n, rules)
		}
		// The interface's own traffic, the blocked networks, port 25.
		if got, want := countRules(iptablesRules(t, f.tool, e.ForwardChain())), 1+len(f.blocked)+1; got != want {
			t.Errorf("%s %s has %d rules, want %d", f.tool, e.ForwardChain(), got, want)
		}
		// Replies, DNS to the resolver, the API port, the drop.
		if got, want := countRules(iptablesRules(t, f.tool, e.InputChain())), 1+f.dns+1+1; got != want {
			t.Errorf("%s %s has %d rules, want %d", f.tool, e.InputChain(), got, want)
		}
	}

	e.Down()
	noChainsLeft(t)
}

func iptablesRules(t *testing.T, tool, chain string) string {
	t.Helper()

	out, err := exec.Command(tool, "-S", chain).CombinedOutput()
	if err != nil {
		t.Fatalf("%s -S %s: %v\n%s", tool, chain, err, out)
	}

	return string(out)
}

func countRules(listing string) int {
	return strings.Count(listing, "\n-A ")
}
