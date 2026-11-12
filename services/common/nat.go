// SPDX-License-Identifier: Apache-2.0

package common

import (
	"net"
	"strings"
)

// NAT is the forwarding and masquerading rule set a tunnel interface needs:
// packets leaving it are accepted, replies coming back into it are accepted
// whatever the FORWARD policy is, and traffic from the tunnel's own networks
// is masqueraded out of the uplink. OpenVPN installs it with Up; WireGuard
// and AmneziaWG hand it to wg-quick as PostUp and PostDown (Hooks).
type NAT struct {
	Interface string
	Uplink    string
	// Networks are the tunnel's networks, one per address family it
	// carries; only traffic from them is masqueraded.
	Networks []*net.IPNet
}

// RunCommand executes one rule command within ToolTimeout; a variable so
// tests can record it.
var RunCommand = func(name string, args ...string) error {
	return RunTool(ToolTimeout, name, args...)
}

// natRule is one rule: its tool, table ("" for filter), chain and match.
type natRule struct {
	tool, table, chain string
	spec               []string
}

func (r natRule) args(action string) []string {
	var args []string
	if r.table != "" {
		args = append(args, "-t", r.table)
	}

	return append(append(args, action, r.chain), r.spec...)
}

func (n NAT) rules() []natRule {
	var rules []natRule
	for _, network := range n.Networks {
		tool := "iptables"
		if network.IP.To4() == nil {
			tool = "ip6tables"
		}
		rules = append(rules,
			natRule{tool, "", "FORWARD", []string{"-i", n.Interface, "-j", "ACCEPT"}},
			natRule{tool, "", "FORWARD", []string{"-o", n.Interface, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"}},
			natRule{tool, "nat", "POSTROUTING", []string{"-s", network.String(), "-o", n.Uplink, "-j", "MASQUERADE"}},
		)
	}

	return rules
}

// Up installs the rules, each only where it is not already: a node that
// died without Down left them behind.
func (n NAT) Up() error {
	for _, r := range n.rules() {
		if RunQuiet(r.tool, r.args("-C")...) == nil {
			continue
		}
		if err := run(r.tool, r.args("-A")...); err != nil {
			return err
		}
	}

	return nil
}

// Down removes the rules; a rule already gone is not an error.
func (n NAT) Down() {
	for _, r := range n.rules() {
		_ = RunCommand(r.tool, r.args("-D")...)
	}
}

// Hooks renders the rules as the PostUp and PostDown lines of a wg-quick
// configuration, which bash runs: PostUp adds each rule only where it is not
// already, PostDown removes them, and every call waits for the xtables lock
// as the node's own do. Every word is an interface name that passed
// CheckInterfaceName, a network or iptables' own, so none needs quoting.
func (n NAT) Hooks() (up, down string) {
	var ups, downs []string
	for _, r := range n.rules() {
		ups = append(ups, xtables(r.tool, r.args("-C"))+" 2>/dev/null || "+xtables(r.tool, r.args("-A")))
		downs = append(downs, xtables(r.tool, r.args("-D")))
	}

	return strings.Join(ups, "; ") + ";", strings.Join(downs, "; ") + ";"
}

func xtables(tool string, args []string) string {
	return tool + " -w " + xtablesWait + " " + strings.Join(args, " ")
}
