// SPDX-License-Identifier: Apache-2.0

package types

import (
	"net"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/viper"

	wgtypes "github.com/Feryx-Official/dvpnd/v9/services/wireguard/types"
)

func public(t *testing.T, private string) []byte {
	t.Helper()

	key, err := wgtypes.KeyFromString(private)
	if err != nil {
		t.Fatal(err)
	}

	return key.Public().Bytes()
}

// Rules: [CT-8].
func TestDeriveNetworks(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		key, err := wgtypes.NewPrivateKey()
		if err != nil {
			t.Fatal(err)
		}
		n := DeriveNetworks(key.Public().Bytes())
		if again := DeriveNetworks(key.Public().Bytes()); again.IPv4.String() != n.IPv4.String() || again.IPv6.String() != n.IPv6.String() {
			t.Fatal("a key must always derive the same networks")
		}

		v4 := n.IPv4.IP.To4()
		if ones, _ := n.IPv4.Mask.Size(); v4 == nil || v4[0] != 10 || v4[1] < 11 || v4[1] > 250 || v4[3] != 0 || ones != 24 {
			t.Fatalf("ipv4 network %s: must be a 10.x.y.0/24 with x from 11 to 250", n.IPv4)
		}
		if ones, _ := n.IPv6.Mask.Size(); n.IPv6.IP[0] != 0xfd || ones != 120 || !n.IPv6.IP.Equal(n.IPv6.IP.Mask(n.IPv6.Mask)) {
			t.Fatalf("ipv6 network %s: must be a unique local /120", n.IPv6)
		}
		seen[n.IPv4.String()] = true
	}
	if len(seen) < 190 {
		t.Fatalf("200 keys derived only %d distinct networks", len(seen))
	}
}

// Rules: [CT-9].
func TestNetworksPool(t *testing.T) {
	_, v4, _ := net.ParseCIDR("10.20.30.0/24")
	_, v6, _ := net.ParseCIDR("fd01:2:3::/120")

	a, b, err := Networks{IPv4: v4, IPv6: v6}.Pool().Get()
	if err != nil || a.IP().String() != "10.20.30.2" || b.IP().String() != "fd01:2:3::2" {
		t.Fatalf("first peer addresses %s %s (%v): the interface keeps .1", a.IP(), b.IP(), err)
	}
}

// Rules: [CT-8].
func TestTunnelNetworks(t *testing.T) {
	c := NewConfig().WithDefaultValues()

	// Empty settings: each tier's networks come from its own key.
	def, v3, err := c.TunnelNetworks()
	if err != nil {
		t.Fatal(err)
	}
	if want := DeriveNetworks(public(t, c.PrivateKey)); def.IPv4.String() != want.IPv4.String() || def.IPv6.String() != want.IPv6.String() {
		t.Fatalf("default tier %s %s, want %s %s", def.IPv4, def.IPv6, want.IPv4, want.IPv6)
	}
	if want := DeriveNetworks(public(t, c.V3.PrivateKey)); v3.IPv4.String() != want.IPv4.String() || v3.IPv6.String() != want.IPv6.String() {
		t.Fatalf("3.1 tier %s %s, want %s %s", v3.IPv4, v3.IPv6, want.IPv4, want.IPv6)
	}

	// Settings replace them; host bits are dropped.
	c.IPv4Subnet, c.IPv6Subnet = "192.168.77.9/24", "fd00:77::/120"
	if def, _, err = c.TunnelNetworks(); err != nil || def.IPv4.String() != "192.168.77.0/24" || def.IPv6.String() != "fd00:77::/120" {
		t.Fatalf("set subnets: %s %s (%v)", def.IPv4, def.IPv6, err)
	}

	// A derived 3.1 network that meets the default tier's moves aside.
	c.IPv4Subnet = DeriveNetworks(public(t, c.V3.PrivateKey)).IPv4.String()
	def, v3, err = c.TunnelNetworks()
	if err != nil || def.Overlaps(v3) {
		t.Fatalf("a colliding derived network must move: %s and %s (%v)", def.IPv4, v3.IPv4, err)
	}

	// Set subnets that overlap are refused.
	c.V3.IPv4Subnet = c.IPv4Subnet
	if _, _, err = c.TunnelNetworks(); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("overlapping subnets: %v", err)
	}
	if c.Validate() == nil {
		t.Fatal("Validate must refuse overlapping subnets")
	}

	// With the 3.1 tier off only the default tier's networks exist.
	c.V3.Enabled = false
	if _, v3, err = c.TunnelNetworks(); err != nil || v3.IPv4 != nil {
		t.Fatalf("3.1 tier off: %v %v", v3.IPv4, err)
	}
}

// Rules: [CT-8].
func TestSubnetSettingsRoundTrip(t *testing.T) {
	c := NewConfig().WithDefaultValues()
	c.IPv4Subnet, c.IPv6Subnet = "10.44.55.0/24", "fd44:55::/120"
	c.V3.IPv4Subnet, c.V3.IPv6Subnet = "10.44.56.0/24", "fd44:56::/120"

	path := filepath.Join(t.TempDir(), ConfigFileName)
	if err := c.SaveToPath(path); err != nil {
		t.Fatal(err)
	}
	vp := viper.New()
	vp.SetConfigFile(path)
	read, err := ReadInConfig(vp)
	if err != nil {
		t.Fatal(err)
	}
	if read.IPv4Subnet != c.IPv4Subnet || read.IPv6Subnet != c.IPv6Subnet ||
		read.V3.IPv4Subnet != c.V3.IPv4Subnet || read.V3.IPv6Subnet != c.V3.IPv6Subnet {
		t.Fatalf("subnets lost in the round trip: %+v %+v", read, read.V3)
	}

	// A file written before the settings existed validates and derives.
	older := regexp.MustCompile(`(?m)^ipv[46]_subnet = .*\n`).ReplaceAllString(NewConfig().WithDefaultValues().String(), "")
	if err := writeFile(path, older); err != nil {
		t.Fatal(err)
	}
	vp = viper.New()
	vp.SetConfigFile(path)
	if read, err = ReadInConfig(vp); err != nil {
		t.Fatal(err)
	}
	if err := read.Validate(); err != nil || read.IPv4Subnet != "" || read.V3.IPv4Subnet != "" {
		t.Fatalf("a file without subnet settings: %v %+v", err, read)
	}
}

// Rules: [CT-8].
func TestSubnetValidation(t *testing.T) {
	for _, tc := range []struct {
		v4, v6 string
		ok     bool
	}{
		{"10.1.2.0/24", "", true},
		{"172.20.0.0/16", "fd12:3456:789a::/64", true},
		{"192.168.9.0/28", "fd00::/124", true},
		{"8.8.8.0/24", "", false},    // not private
		{"10.0.0.0/8", "", false},    // too big
		{"10.1.2.0/29", "", false},   // too small
		{"fd00::/120", "", false},    // wrong family
		{"", "10.1.2.0/24", false},   // wrong family
		{"", "2001:db8::/64", false}, // not private
		{"", "fd00::/48", false},     // too big
		{"", "fd00::/126", false},    // too small
		{"banana", "", false},
	} {
		c := NewConfig().WithDefaultValues()
		c.IPv4Subnet, c.IPv6Subnet = tc.v4, tc.v6
		if err := c.Validate(); (err == nil) != tc.ok {
			t.Errorf("ipv4_subnet %q ipv6_subnet %q: %v", tc.v4, tc.v6, err)
		}
	}
}
