// SPDX-License-Identifier: Apache-2.0

package common

import (
	"errors"
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Rules: [EG-2], [EG-3].
func TestBlockedNetworks(t *testing.T) {
	var nets []*net.IPNet
	for _, cidr := range BlockedNetworksV4 {
		ip, n, err := net.ParseCIDR(cidr)
		if err != nil || ip.To4() == nil {
			t.Fatalf("%s is not an IPv4 CIDR: %v", cidr, err)
		}
		nets = append(nets, n)
	}
	for _, cidr := range BlockedNetworksV6 {
		ip, n, err := net.ParseCIDR(cidr)
		if err != nil || ip.To4() != nil {
			t.Fatalf("%s is not an IPv6 CIDR: %v", cidr, err)
		}
		nets = append(nets, n)
	}
	if got := BlockedNetworks(); len(got) != len(nets) || got[0] != BlockedNetworksV4[0] {
		t.Fatalf("BlockedNetworks: %v", got)
	}

	blocked := func(addr string) bool {
		ip := net.ParseIP(addr)
		for _, n := range nets {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}

	for _, addr := range []string{
		"0.0.0.0", "127.0.0.1", "127.8.9.10", "10.8.0.1", "10.9.0.2", "172.17.0.1", "192.168.1.1",
		"169.254.169.254", "100.100.100.200", "192.0.0.170", "198.18.0.1", "224.0.0.251",
		"255.255.255.255", "::", "::1", "fd86:ea04:1115::1", "fe80::1", "ff02::1",
		// An IPv4-mapped IPv6 literal is matched as the IPv4 address it carries.
		"::ffff:127.0.0.1", "::ffff:169.254.169.254",
	} {
		if !blocked(addr) {
			t.Errorf("%s is not blocked", addr)
		}
	}

	// Public destinations stay reachable. A rule such as ::ffff:0:0/96 would
	// match every IPv4 address in Go's net.IPNet, which Hysteria matches with:
	// IPv4 addresses must never fall into an IPv6 entry.
	for _, addr := range []string{"1.1.1.1", "8.8.8.8", "93.184.215.14", "2606:4700:4700::1111", "2a01:4f8::1"} {
		if blocked(addr) {
			t.Errorf("%s is blocked", addr)
		}
	}
}

// fakeTables is enough of iptables and ip6tables for the egress chains: it
// keeps each table's chains and their rules and fails where the real tools
// fail.
type fakeTables struct {
	chains map[string]map[string][]string // tool → chain → rules
	calls  []string
}

func newFakeTables() *fakeTables {
	f := &fakeTables{chains: map[string]map[string][]string{}}
	for _, tool := range []string{"iptables", "ip6tables"} {
		f.chains[tool] = map[string][]string{"FORWARD": {}, "INPUT": {}, "OUTPUT": {}}
	}

	return f
}

func (f *fakeTables) run(name string, args ...string) error {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))

	chains, ok := f.chains[name]
	if !ok {
		return errors.New("unknown tool " + name)
	}
	op, chain := args[0], args[1]
	rule := strings.Join(args[2:], " ")
	rules, exists := chains[chain]

	switch op {
	case "-N":
		if exists {
			return errors.New("chain already exists")
		}
		chains[chain] = []string{}
	case "-F":
		if !exists {
			return errors.New("no chain")
		}
		chains[chain] = []string{}
	case "-X":
		if !exists || len(rules) > 0 {
			return errors.New("no chain, or not empty")
		}
		for _, other := range chains {
			for _, r := range other {
				if strings.HasSuffix(r, "-j "+chain) {
					return errors.New("chain is referenced")
				}
			}
		}
		delete(chains, chain)
	case "-A":
		if !exists {
			return errors.New("no chain")
		}
		chains[chain] = append(rules, rule)
	case "-I":
		pos, err := strconv.Atoi(args[2])
		if !exists || err != nil || pos != 1 {
			return errors.New("bad insert")
		}
		rule = strings.Join(args[3:], " ")
		chains[chain] = append([]string{rule}, rules...)
	case "-C":
		if !slices.Contains(rules, rule) {
			return errors.New("no such rule")
		}
	case "-D":
		i := slices.Index(rules, rule)
		if i < 0 {
			return errors.New("no such rule")
		}
		chains[chain] = slices.Delete(rules, i, i+1)
	default:
		return errors.New("unexpected " + op)
	}

	return nil
}

func fakeIptables(t *testing.T) *fakeTables {
	t.Helper()

	f := newFakeTables()
	saved, savedQuiet := RunCommand, RunQuiet
	RunCommand, RunQuiet = f.run, f.run
	t.Cleanup(func() { RunCommand, RunQuiet = saved, savedQuiet })

	return f
}

func withEgress(t *testing.T, e Egress) {
	t.Helper()

	saved := EgressPolicy()
	SetEgress(e)
	t.Cleanup(func() { SetEgress(saved) })
}

// Rules: [EG-8], [EG-9].
func TestTunnelEgressUpDown(t *testing.T) {
	f := fakeIptables(t)
	withEgress(t, Egress{APIPort: 8585, Resolver: net.ParseIP("10.8.0.1")})

	e := TunnelEgress{Interface: "wg0"}
	if err := e.Up(); err != nil {
		t.Fatal(err)
	}

	for _, tool := range []string{"iptables", "ip6tables"} {
		chains := f.chains[tool]

		// The jumps are the first rules of FORWARD and INPUT, for the interface only.
		if got := chains["FORWARD"]; len(got) != 1 || got[0] != "-i wg0 -j DVPND-FWD-wg0" {
			t.Fatalf("%s FORWARD: %v", tool, got)
		}
		if got := chains["INPUT"]; len(got) != 1 || got[0] != "-i wg0 -j DVPND-IN-wg0" {
			t.Fatalf("%s INPUT: %v", tool, got)
		}

		blocked := BlockedNetworksV4
		if tool == "ip6tables" {
			blocked = BlockedNetworksV6
		}
		want := []string{"-o wg0 -j DROP"}
		for _, cidr := range blocked {
			want = append(want, "-d "+cidr+" -j DROP")
		}
		want = append(want, "-p tcp --dport 25 -j DROP")
		if got := chains["DVPND-FWD-wg0"]; !slices.Equal(got, want) {
			t.Fatalf("%s forward chain:\n%s\nwant:\n%s", tool, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}

		want = []string{"-m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT"}
		if tool == "iptables" { // the resolver has an IPv4 address
			want = append(want,
				"-d 10.8.0.1 -p udp --dport 53 -j ACCEPT",
				"-d 10.8.0.1 -p tcp --dport 53 -j ACCEPT")
		}
		want = append(want, "-p tcp --dport 8585 -j ACCEPT", "-j DROP")
		if got := chains["DVPND-IN-wg0"]; !slices.Equal(got, want) {
			t.Fatalf("%s input chain:\n%s\nwant:\n%s", tool, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}

	// A node that died without Down starts again: the chains are refilled,
	// not duplicated, and the jumps are not added twice.
	if err := e.Up(); err != nil {
		t.Fatal(err)
	}
	if got := f.chains["iptables"]["FORWARD"]; len(got) != 1 {
		t.Fatalf("FORWARD after a second Up: %v", got)
	}
	if got := len(f.chains["iptables"]["DVPND-FWD-wg0"]); got != len(BlockedNetworksV4)+2 {
		t.Fatalf("forward chain after a second Up has %d rules", got)
	}

	e.Down()
	for _, tool := range []string{"iptables", "ip6tables"} {
		if len(f.chains[tool]) != 3 || len(f.chains[tool]["FORWARD"]) != 0 || len(f.chains[tool]["INPUT"]) != 0 {
			t.Fatalf("%s after Down: %v", tool, f.chains[tool])
		}
	}

	// Down again, with nothing left, is quiet.
	e.Down()
}

// Rules: [EG-1], [EG-4], [EG-8].
func TestTunnelEgressPolicy(t *testing.T) {
	f := fakeIptables(t)
	withEgress(t, Egress{AllowSMTP: true})

	if err := (TunnelEgress{Interface: "ovpn0"}).Up(); err != nil {
		t.Fatal(err)
	}

	fwd := f.chains["iptables"]["DVPND-FWD-ovpn0"]
	if slices.Contains(fwd, "-p tcp --dport 25 -j DROP") {
		t.Fatalf("allow_smtp is on, yet port 25 is dropped: %v", fwd)
	}
	// No resolver and no API port: only replies come back in.
	in := f.chains["iptables"]["DVPND-IN-ovpn0"]
	if !slices.Equal(in, []string{"-m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT", "-j DROP"}) {
		t.Fatalf("input chain: %v", in)
	}
}

// Rules: [EG-11].
func TestTunnelEgressWithoutIPv6(t *testing.T) {
	f := fakeIptables(t)
	withEgress(t, Egress{})

	saved := ipv6Present
	ipv6Present = filepath.Join(t.TempDir(), "absent")
	t.Cleanup(func() { ipv6Present = saved })

	e := TunnelEgress{Interface: "wg0"}
	if err := e.Up(); err != nil {
		t.Fatal(err)
	}
	e.Down()
	for _, call := range f.calls {
		if strings.HasPrefix(call, "ip6tables") {
			t.Fatalf("ip6tables called on a kernel without IPv6: %s", call)
		}
	}
}

// Rules: [EG-10].
func TestTunnelEgressFailure(t *testing.T) {
	f := fakeIptables(t)
	withEgress(t, Egress{})

	// An iptables that cannot append (a missing module, say) fails Up with
	// the command in the message.
	RunCommand = func(name string, args ...string) error {
		if args[0] == "-A" {
			return errors.New("exit status 1")
		}
		return f.run(name, args...)
	}

	err := (TunnelEgress{Interface: "wg0"}).Up()
	if err == nil || !strings.Contains(err.Error(), "iptables -A DVPND-FWD-wg0 -o wg0 -j DROP") {
		t.Fatalf("Up: %v", err)
	}
}

// Rules: [EG-9].
func TestChainNamesFitIptables(t *testing.T) {
	e := TunnelEgress{Interface: strings.Repeat("x", 15)} // IFNAMSIZ - 1
	for _, name := range []string{e.ForwardChain(), e.InputChain()} {
		if len(name) > 28 {
			t.Errorf("%s is %d characters; iptables allows 28", name, len(name))
		}
	}
}

// TestChainThatCannotBeCreated: -N fails when the chain is left from an
// earlier run, and the chain is emptied then; when emptying it fails too, -N
// failed for another reason (the xtables lock, a missing module), which the
// error names instead of the flush's "no chain".
func TestChainThatCannotBeCreated(t *testing.T) {
	f := fakeIptables(t)
	withEgress(t, Egress{})

	RunQuiet = func(name string, args ...string) error {
		if args[0] == "-N" {
			return errors.New("exit status 4")
		}
		return f.run(name, args...)
	}
	RunCommand = func(name string, args ...string) error {
		if args[0] == "-F" {
			return errors.New("exit status 1")
		}
		return f.run(name, args...)
	}

	err := (TunnelEgress{Interface: "wg0"}).Up()
	if err == nil || !strings.Contains(err.Error(), "iptables -N DVPND-FWD-wg0: exit status 4") {
		t.Fatalf("Up: %v", err)
	}
}
