// SPDX-License-Identifier: Apache-2.0

package wireguard

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	wgtypes "github.com/Feryx-Official/dvpnd/v9/services/wireguard/types"
)

func renderConfig(t *testing.T, cfg *wgtypes.Config) string {
	t.Helper()

	v4, _ := wgtypes.NewIPv4PoolFromCIDR("10.8.0.2/24")
	v6, _ := wgtypes.NewIPv6PoolFromCIDR("fd86:ea04:1115::2/120")

	out, err := renderInterface(cfg, wgtypes.NewIPPool(v4, v6), []string{"Jc = 4", "H1 = 12345"})
	if err != nil {
		t.Fatal(err)
	}

	return string(out)
}

// Rules: [CT-9].
func TestTunnelAddress(t *testing.T) {
	v4, _ := wgtypes.NewIPv4PoolFromCIDR("10.9.0.2/24")
	v6, _ := wgtypes.NewIPv6PoolFromCIDR("fd86:ea04:1116::2/120")
	if got := TunnelAddress(wgtypes.NewIPPool(v4, v6), true); got != "10.9.0.1/24,fd86:ea04:1116::1/120" {
		t.Fatalf("tunnel address: %s", got)
	}
	if got := TunnelAddress(wgtypes.NewIPPool(v4, v6), false); got != "10.9.0.1/24" {
		t.Fatalf("tunnel address without IPv6: %s", got)
	}
	// The Handshake resolver listens on the same IPv4 address the interface gets.
	if got := NewVariant(Default, wgtypes.NewIPPool(v4, v6)).TunnelIPv4().String(); got != "10.9.0.1" {
		t.Fatalf("tunnel IPv4: %s", got)
	}
}

// Rules: [RT-8].
func TestConfigTemplateForwardRules(t *testing.T) {
	cfg := wgtypes.NewConfig().WithDefaultValues()
	cfg.ListenPort = 51820
	cfg.PrivateKey = "cHJpdmF0ZQ=="
	cfg.Uplink = "eth0"

	out := renderConfig(t, cfg)

	lines := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(l, " = "); ok {
			lines[k] = v
		}
	}

	if lines["ListenPort"] != "51820" {
		t.Fatalf("ListenPort = %q", lines["ListenPort"])
	}
	if lines["Address"] != "10.8.0.1/24,fd86:ea04:1115::1/120" {
		t.Fatalf("Address = %q", lines["Address"])
	}
	if lines["Jc"] != "4" || lines["H1"] != "12345" {
		t.Fatalf("extra interface lines missing:\n%s", out)
	}
	if !strings.HasPrefix(out, "[Interface]\n") || strings.Contains(out, "\n\n[") {
		t.Fatalf("layout:\n%s", out)
	}

	for tool, network := range map[string]string{"iptables": "10.8.0.0/24", "ip6tables": "fd86:ea04:1115::/120"} {
		want := []string{
			tool + " -w 5 -A FORWARD -i %i -j ACCEPT;",
			tool + " -w 5 -A FORWARD -o %i -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT;",
			tool + " -w 5 -t nat -A POSTROUTING -s " + network + " -o eth0 -j MASQUERADE;",
		}
		for _, rule := range want {
			if !strings.Contains(lines["PostUp"], rule) {
				t.Errorf("PostUp lacks %q", rule)
			}
			down := strings.Replace(rule, " -A ", " -D ", 1)
			if !strings.Contains(lines["PostDown"], down) {
				t.Errorf("PostDown lacks %q", down)
			}
		}
	}

	// The accept rules come before the NAT rule so a reply is accepted
	// whatever the chain policy is.
	up := lines["PostUp"]
	if strings.Index(up, "-o %i -m conntrack") > strings.Index(up, "MASQUERADE") {
		t.Errorf("return-path rule must precede MASQUERADE: %s", up)
	}
}

// runHook runs a PostUp or PostDown line the way wg-quick does, in bash
// with %i replaced, against fake iptables and ip6tables that log their
// arguments and answer -C with check: 0 when the rule is there.
func runHook(t *testing.T, hook, iface string, check int) []string {
	t.Helper()

	bin := t.TempDir()
	log := filepath.Join(bin, "calls.log")
	for _, tool := range []string{"iptables", "ip6tables"} {
		script := fmt.Sprintf("#!/bin/sh\necho \"%s $*\" >> %s\ncase \" $* \" in *\" -C \"*) exit %d;; esac\n", tool, log, check)
		if err := os.WriteFile(filepath.Join(bin, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "-e", "-c", strings.ReplaceAll(hook, "%i", iface))
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s\n%s", err, out, hook)
	}
	raw, _ := os.ReadFile(log)

	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func hookLine(t *testing.T, out, key string) string {
	t.Helper()

	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(l, key+" = "); ok {
			return v
		}
	}
	t.Fatalf("no %s in:\n%s", key, out)

	return ""
}

// TestPostUpAddsScopedRulesOnce: PostUp masquerades only the tunnel's own
// networks, adds a rule only where an interface left by a node that died
// has not left it already, and PostDown removes the same rules.
//
// Rules: [EG-9], [EG-14].
func TestPostUpAddsScopedRulesOnce(t *testing.T) {
	cfg := wgtypes.NewConfig().WithDefaultValues()
	cfg.Uplink = "eth0"
	out := renderConfig(t, cfg)

	added := strings.Join(runHook(t, hookLine(t, out, "PostUp"), "wg0", 1), "\n")
	for _, rule := range []string{
		"iptables -w 5 -t nat -A POSTROUTING -s 10.8.0.0/24 -o eth0 -j MASQUERADE",
		"ip6tables -w 5 -t nat -A POSTROUTING -s fd86:ea04:1115::/120 -o eth0 -j MASQUERADE",
		"iptables -w 5 -A FORWARD -i wg0 -j ACCEPT",
		"ip6tables -w 5 -A FORWARD -o wg0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
	} {
		if !strings.Contains(added, rule+"\n") && !strings.HasSuffix(added, rule) {
			t.Errorf("PostUp on a clean host lacks %q:\n%s", rule, added)
		}
	}
	if strings.Contains(added, "POSTROUTING -o") {
		t.Errorf("PostUp masquerades every source:\n%s", added)
	}

	for _, call := range runHook(t, hookLine(t, out, "PostUp"), "wg0", 0) {
		if strings.Contains(call, " -A ") {
			t.Errorf("PostUp adds a rule that is already there: %s", call)
		}
	}

	removed := strings.Join(runHook(t, hookLine(t, out, "PostDown"), "wg0", 0), "\n")
	if !strings.Contains(removed, "iptables -w 5 -t nat -D POSTROUTING -s 10.8.0.0/24 -o eth0 -j MASQUERADE") {
		t.Errorf("PostDown leaves the masquerade rule:\n%s", removed)
	}
}

// TestIPv6OffMeansNoIPv6: with enable_ipv6 = false the interface gets no
// IPv6 address and PostUp calls no ip6tables, which fails on a kernel
// without IPv6.
//
// Rules: [EG-11].
func TestIPv6OffMeansNoIPv6(t *testing.T) {
	cfg := wgtypes.NewConfig().WithDefaultValues()
	cfg.Uplink = "eth0"
	cfg.EnableIPv6 = false
	out := renderConfig(t, cfg)

	if a := hookLine(t, out, "Address"); a != "10.8.0.1/24" {
		t.Errorf("Address = %q, want the IPv4 address only", a)
	}
	for _, key := range []string{"PostUp", "PostDown"} {
		if line := hookLine(t, out, key); strings.Contains(line, "ip6tables") || !strings.Contains(line, "iptables") {
			t.Errorf("%s = %s", key, line)
		}
	}
}
