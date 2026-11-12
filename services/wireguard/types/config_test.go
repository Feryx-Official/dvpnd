// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

func TestConfigDefaultsToDualStack(t *testing.T) {
	c := NewConfig().WithDefaultValues()
	if !c.EnableIPv6 {
		t.Fatal("IPv6 must be on by default, as on every upstream node")
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("default config must validate: %v", err)
	}
	out := c.String()
	if !strings.Contains(out, "enable_ipv6 = true") {
		t.Errorf("rendered config lacks enable_ipv6, got:\n%s", out)
	}
}

// TestConfigRefusesUnsafeNames: the interface and uplink names go into
// commands and into the PostUp line wg-quick runs in a root shell; only a
// name the kernel accepts for an interface, without shell syntax, passes.
//
// Rules: [RT-17].
func TestConfigRefusesUnsafeNames(t *testing.T) {
	bad := []string{"wg0;id", "wg0 x", "$(id)", "`id`", "a/b", "wg0\n", "wg-0123456789abc"}
	for _, name := range bad {
		c := NewConfig().WithDefaultValues()
		c.Interface = name
		if err := c.Validate(); err == nil {
			t.Errorf("interface %q accepted", name)
		}

		c = NewConfig().WithDefaultValues()
		c.Uplink = name
		if err := c.Validate(); err == nil {
			t.Errorf("uplink %q accepted", name)
		}
	}

	for _, name := range []string{"eth0", "enp3s0", "br-1a2b.100", "wg_0", "wg-0123456789ab"} {
		c := NewConfig().WithDefaultValues()
		c.Interface, c.Uplink = name, name
		if err := c.Validate(); err != nil {
			t.Errorf("%q refused: %v", name, err)
		}
	}

	c := NewConfig().WithDefaultValues()
	c.Uplink = ""
	if err := c.Validate(); err != nil {
		t.Errorf("an empty uplink means detect it: %v", err)
	}
}
