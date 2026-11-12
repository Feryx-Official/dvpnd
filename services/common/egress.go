// SPDX-License-Identifier: Apache-2.0

package common

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// BlockedNetworksV4 and BlockedNetworksV6 are the destinations no client may
// reach through the node: "this network" and loopback (the host's own
// services, the proxies' control APIs among them), the private, shared and
// link-local ranges (the provider's metadata service, the networks around
// the host, other clients on the node's tunnel), benchmarking, multicast and
// the reserved space. Every service renders its rules from these two lists,
// so the protocols cannot drift apart.
var (
	BlockedNetworksV4 = []string{
		"0.0.0.0/8",
		"10.0.0.0/8",
		"100.64.0.0/10",
		"127.0.0.0/8",
		"169.254.0.0/16",
		"172.16.0.0/12",
		"192.0.0.0/24",
		"192.168.0.0/16",
		"198.18.0.0/15",
		"224.0.0.0/4",
		"240.0.0.0/4",
	}
	BlockedNetworksV6 = []string{
		"::/128",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
		"ff00::/8",
	}
)

// BlockedNetworks is both lists, IPv4 first.
func BlockedNetworks() []string {
	return append(append([]string(nil), BlockedNetworksV4...), BlockedNetworksV6...)
}

// JSON renders v as JSON, for the templates that write the blocked networks
// into a proxy's configuration; a JSON array is also a YAML flow sequence.
func JSON(v interface{}) (string, error) {
	b, err := json.Marshal(v)

	return string(b), err
}

// SMTPPort is the mail relay port, blocked unless the operator allows it:
// a node that lets anyone send mail from its address ends up on block lists.
const SMTPPort = 25

// BlockedLocalDomain is the name blocked by domain wherever a service can
// match names: localhost and every name under it resolve to loopback.
const BlockedLocalDomain = "localhost"

// Egress is the node-wide egress policy. The start command sets it once
// before the service's Init (the proxies render it into their
// configuration) and again before Start, when the Handshake resolver's
// address is known (the tunnel services install it as firewall rules).
type Egress struct {
	// AllowSMTP lets clients open TCP connections to port 25.
	AllowSMTP bool
	// APIPort is the node API's TCP port. Tunnel clients may reach it on the
	// node's own addresses, as anyone can from outside; zero blocks it.
	APIPort uint16
	// Resolver is the Handshake resolver's address on the tunnel; tunnel
	// clients may send DNS there. Nil when the resolver is off.
	Resolver net.IP
}

var egress struct {
	sync.RWMutex
	policy Egress
}

// SetEgress replaces the node's egress policy.
func SetEgress(e Egress) {
	egress.Lock()
	defer egress.Unlock()
	egress.policy = e
}

// EgressPolicy is what SetEgress stored; the zero policy blocks SMTP and
// lets tunnel clients reach nothing on the host.
func EgressPolicy() Egress {
	egress.RLock()
	defer egress.RUnlock()

	return egress.policy
}

// ipv6Present is a directory that exists only when the kernel has IPv6; a
// variable so tests can point it elsewhere.
var ipv6Present = "/proc/sys/net/ipv6"

// TunnelEgress keeps a tunnel interface's clients off the host and the
// networks around it. It owns two chains per interface, one jumped to from
// the top of FORWARD and one from the top of INPUT, for traffic coming in on
// the interface:
//
//   - forward: drop traffic back into the tunnel (client to client), to the
//     blocked networks, and to TCP port 25 unless allowed;
//   - input: accept replies, DNS to the Handshake resolver and the node API
//     port; drop everything else, so services bound on all of the host's
//     addresses are not reachable through the tunnel.
//
// The forwarding and masquerading rules (NAT, or WireGuard's PostUp) are
// left as they are; these chains are evaluated before them. Up flushes and
// refills the chains and adds a jump only where none is, so a node that
// died without Down leaves nothing to clean up by hand.
type TunnelEgress struct {
	Interface string
}

// ForwardChain and InputChain are the chain names; iptables allows 28
// characters and an interface name has at most 15.
func (e TunnelEgress) ForwardChain() string { return "DVPND-FWD-" + e.Interface }
func (e TunnelEgress) InputChain() string   { return "DVPND-IN-" + e.Interface }

// family is one iptables binary with the blocked networks of its address
// family.
type family struct {
	tool    string
	v6      bool
	blocked []string
}

// families are the address families the kernel has: IPv4 always, IPv6
// unless the kernel was booted without it.
func families() []family {
	list := []family{{tool: "iptables", blocked: BlockedNetworksV4}}
	if _, err := os.Stat(ipv6Present); err == nil {
		list = append(list, family{tool: "ip6tables", v6: true, blocked: BlockedNetworksV6})
	}

	return list
}

// inFamily reports whether ip belongs to the family.
func (f family) inFamily(ip net.IP) bool {
	return ip != nil && (ip.To4() == nil) == f.v6
}

// chains is a set of iptables chains of the node's own and the jumps into
// them from the built-in chains. up fills the chains before it jumps to
// them, so no packet passes unfiltered while the rules go in; it refills
// chains an earlier run left behind and adds a jump only where none is, so a
// node that died without down leaves nothing to clean up by hand.
type chains struct {
	names []string
	// rules are the rules appended to the chains, in order, for a family.
	rules func(f family) [][]string
	// jumps are the rules inserted at the top of built-in chains, as
	// {builtin, match..., "-j", chain}.
	jumps [][]string
}

func (c chains) up() error {
	for _, f := range families() {
		for _, chain := range c.names {
			// -N fails when the chain is left from an earlier run; then it is
			// emptied instead. When that fails too, -N failed for another
			// reason, which the error names.
			if nerr := RunQuiet(f.tool, "-N", chain); nerr != nil {
				if err := run(f.tool, "-F", chain); err != nil {
					return fmt.Errorf("%s -N %s: %v; %w", f.tool, chain, nerr, err)
				}
			}
		}
		for _, rule := range c.rules(f) {
			if err := run(f.tool, rule...); err != nil {
				return err
			}
		}
		for _, jump := range c.jumps {
			if RunQuiet(f.tool, append([]string{"-C"}, jump...)...) == nil {
				continue
			}
			if err := run(f.tool, append([]string{"-I", jump[0], "1"}, jump[1:]...)...); err != nil {
				return err
			}
		}
	}

	return nil
}

// down removes the jumps and the chains; what is already gone is not an
// error.
func (c chains) down() {
	for _, f := range families() {
		for _, jump := range c.jumps {
			_ = RunQuiet(f.tool, append([]string{"-D"}, jump...)...)
		}
		for _, chain := range c.names {
			_ = RunQuiet(f.tool, "-F", chain)
			_ = RunQuiet(f.tool, "-X", chain)
		}
	}
}

// RunQuiet executes one rule command whose failure is an answer the caller
// handles, not a fault: a chain or a jump that is not there (yet, or any
// more). Its output is discarded so the tool's complaint does not reach the
// node's log; a variable so tests can record it.
var RunQuiet = func(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

func run(tool string, args ...string) error {
	if err := RunCommand(tool, args...); err != nil {
		return fmt.Errorf("%s %s: %w", tool, strings.Join(args, " "), err)
	}

	return nil
}

func (e TunnelEgress) chains(policy Egress) chains {
	fwd, in := e.ForwardChain(), e.InputChain()

	return chains{
		names: []string{fwd, in},
		rules: func(f family) [][]string {
			var rules [][]string

			rules = append(rules, []string{"-A", fwd, "-o", e.Interface, "-j", "DROP"})
			for _, cidr := range f.blocked {
				rules = append(rules, []string{"-A", fwd, "-d", cidr, "-j", "DROP"})
			}
			if !policy.AllowSMTP {
				rules = append(rules, []string{"-A", fwd, "-p", "tcp", "--dport", strconv.Itoa(SMTPPort), "-j", "DROP"})
			}

			rules = append(rules, []string{"-A", in, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"})
			if f.inFamily(policy.Resolver) {
				for _, proto := range []string{"udp", "tcp"} {
					rules = append(rules, []string{"-A", in, "-d", policy.Resolver.String(), "-p", proto, "--dport", "53", "-j", "ACCEPT"})
				}
			}
			if policy.APIPort != 0 {
				rules = append(rules, []string{"-A", in, "-p", "tcp", "--dport", strconv.Itoa(int(policy.APIPort)), "-j", "ACCEPT"})
			}
			rules = append(rules, []string{"-A", in, "-j", "DROP"})

			return rules
		},
		jumps: [][]string{
			{"FORWARD", "-i", e.Interface, "-j", fwd},
			{"INPUT", "-i", e.Interface, "-j", in},
		},
	}
}

// Up installs the chains and their jumps under the current egress policy.
func (e TunnelEgress) Up() error {
	return e.chains(EgressPolicy()).up()
}

// Down removes the jumps and the chains.
func (e TunnelEgress) Down() {
	e.chains(Egress{}).down()
}

// ProxyChain is the chain that holds the proxy account's outgoing traffic.
const ProxyChain = "DVPND-PROXY"

// ProxyEgress is the egress policy in the kernel for a proxy daemon that runs
// as the proxy account: one chain jumped to from the top of OUTPUT for every
// packet that account sends. The proxies apply the policy themselves too,
// but they resolve a name once to match it and again to dial it, so a name
// whose answer changes in between (DNS rebinding) could reach a blocked
// address; the kernel sees only the address actually dialled. The chain
// accepts replies, DNS to the host's resolvers, the loopback ports the daemon
// must reach (Hysteria's authentication hook) and the node API port on the
// host's public addresses. It rejects every other address of the host, which
// the proxies cannot know (services bound to the public address that the
// host's firewall keeps from the outside), the blocked networks, and TCP
// port 25 unless allowed.
//
// DNS is matched on the destination the daemon asked for, before any address
// translation: Docker's embedded resolver answers 127.0.0.11:53 through a
// DNAT to another port.
type ProxyEgress struct {
	UID           uint32
	LoopbackPorts []uint16
}

// resolvConf is where the host's resolvers are listed; a variable so tests
// can point it elsewhere.
var resolvConf = "/etc/resolv.conf"

// nameservers lists the resolvers in resolv.conf.
func nameservers() []net.IP {
	raw, err := os.ReadFile(resolvConf)
	if err != nil {
		return nil
	}

	var list []net.IP
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		host, _, _ := strings.Cut(fields[1], "%") // a link-local zone
		if ip := net.ParseIP(host); ip != nil {
			list = append(list, ip)
		}
	}

	return list
}

func (e ProxyEgress) chains(policy Egress) chains {
	resolvers := nameservers()

	return chains{
		names: []string{ProxyChain},
		rules: func(f family) [][]string {
			rules := [][]string{{"-A", ProxyChain, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"}}
			if !f.v6 {
				for _, port := range e.LoopbackPorts {
					rules = append(rules, []string{"-A", ProxyChain, "-o", "lo", "-d", "127.0.0.1", "-p", "tcp",
						"--dport", strconv.Itoa(int(port)), "-j", "ACCEPT"})
				}
			}
			for _, ip := range resolvers {
				if !f.inFamily(ip) {
					continue
				}
				for _, proto := range []string{"udp", "tcp"} {
					rules = append(rules, []string{"-A", ProxyChain, "-p", proto, "-m", "conntrack",
						"--ctorigdst", ip.String(), "--ctorigdstport", "53", "-j", "ACCEPT"})
				}
			}
			// The blocked networks first, so the API port below is open on
			// the host's public addresses only, never on loopback.
			for _, cidr := range f.blocked {
				rules = append(rules, []string{"-A", ProxyChain, "-d", cidr, "-j", "REJECT"})
			}
			if policy.APIPort != 0 {
				rules = append(rules, []string{"-A", ProxyChain, "-m", "addrtype", "--dst-type", "LOCAL",
					"-p", "tcp", "--dport", strconv.Itoa(int(policy.APIPort)), "-j", "ACCEPT"})
			}
			rules = append(rules, []string{"-A", ProxyChain, "-m", "addrtype", "--dst-type", "LOCAL", "-j", "REJECT"})
			if !policy.AllowSMTP {
				rules = append(rules, []string{"-A", ProxyChain, "-p", "tcp", "--dport", strconv.Itoa(SMTPPort), "-j", "REJECT"})
			}

			return rules
		},
		jumps: [][]string{
			{"OUTPUT", "-m", "owner", "--uid-owner", strconv.FormatUint(uint64(e.UID), 10), "-j", ProxyChain},
		},
	}
}

// Up installs the chain and its jump under the current egress policy.
func (e ProxyEgress) Up() error {
	return e.chains(EgressPolicy()).up()
}

// Down removes the jump and the chain.
func (e ProxyEgress) Down() {
	e.chains(Egress{}).down()
}
