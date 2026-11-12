// SPDX-License-Identifier: Apache-2.0

package wireguard

import (
	"bytes"
	"strings"
	"testing"
	"text/template"

	wgtypes "github.com/Feryx-Official/dvpnd/v9/services/wireguard/types"
)

func renderConfig(t *testing.T, cfg *wgtypes.Config) string {
	t.Helper()

	tmpl, err := template.New("wireguard_conf").Parse(configTemplate)
	if err != nil {
		t.Fatal(err)
	}

	v4, _ := wgtypes.NewIPv4PoolFromCIDR("10.8.0.2/24")
	v6, _ := wgtypes.NewIPv6PoolFromCIDR("fd86:ea04:1115::2/120")

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, interfaceConfig{
		Config:  cfg,
		Address: TunnelAddress(wgtypes.NewIPPool(v4, v6)),
		Extra:   []string{"Jc = 4", "H1 = 12345"},
	}); err != nil {
		t.Fatal(err)
	}

	return buf.String()
}

// Rules: [CT-9].
func TestTunnelAddress(t *testing.T) {
	v4, _ := wgtypes.NewIPv4PoolFromCIDR("10.9.0.2/24")
	v6, _ := wgtypes.NewIPv6PoolFromCIDR("fd86:ea04:1116::2/120")
	if got := TunnelAddress(wgtypes.NewIPPool(v4, v6)); got != "10.9.0.1/24,fd86:ea04:1116::1/120" {
		t.Fatalf("tunnel address: %s", got)
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

	for _, tool := range []string{"iptables", "ip6tables"} {
		want := []string{
			tool + " -w 5 -A FORWARD -i %i -j ACCEPT;",
			tool + " -w 5 -A FORWARD -o %i -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT;",
			tool + " -w 5 -t nat -A POSTROUTING -o eth0 -j MASQUERADE;",
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
