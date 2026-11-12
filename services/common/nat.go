// SPDX-License-Identifier: Apache-2.0

package common

import (
	"fmt"
	"strings"
)

// NAT is the forwarding and masquerading rule set a tunnel interface needs:
// packets leaving it are accepted, replies coming back into it are accepted
// whatever the FORWARD policy is, and peer traffic is masqueraded out of the
// uplink. It is the rule set WireGuard's PostUp installs, for services that
// have no PostUp hook of their own.
type NAT struct {
	Interface string
	Uplink    string
}

// RunCommand executes one rule command within ToolTimeout; a variable so
// tests can record it.
var RunCommand = func(name string, args ...string) error {
	return RunTool(ToolTimeout, name, args...)
}

func (n NAT) rules(action string) [][]string {
	var rules [][]string
	for _, tool := range []string{"iptables", "ip6tables"} {
		rules = append(rules,
			[]string{tool, action, "FORWARD", "-i", n.Interface, "-j", "ACCEPT"},
			[]string{tool, action, "FORWARD", "-o", n.Interface, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
			[]string{tool, "-t", "nat", action, "POSTROUTING", "-o", n.Uplink, "-j", "MASQUERADE"},
		)
	}

	return rules
}

// Up installs the rules.
func (n NAT) Up() error {
	for _, rule := range n.rules("-A") {
		if err := RunCommand(rule[0], rule[1:]...); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(rule, " "), err)
		}
	}

	return nil
}

// Down removes the rules; a rule already gone is not an error.
func (n NAT) Down() {
	for _, rule := range n.rules("-D") {
		_ = RunCommand(rule[0], rule[1:]...)
	}
}
