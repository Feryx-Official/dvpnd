// SPDX-License-Identifier: Apache-2.0

package common

import (
	"testing"
)

// TestCheckInterfaceName: a name that reaches iptables, a daemon's
// configuration or a root shell is a plain interface name, never shell
// syntax or something iptables reads as an option.
//
// Rules: [RT-17].
func TestCheckInterfaceName(t *testing.T) {
	for _, name := range []string{"eth0", "wg0", "awg1", "ovpn0", "enp3s0", "br-1a2b.100", "veth_1", "wg-0123456789ab"} {
		if err := CheckInterfaceName("interface", name); err != nil {
			t.Errorf("%q refused: %v", name, err)
		}
	}
	for _, name := range []string{"", "-j", ".", "..", "wg0;id", "eth0 x", "$(id)", "`id`", "a/b", "wg0\n", "a|b", "a&b", "wg-0123456789abc"} {
		if err := CheckInterfaceName("interface", name); err == nil {
			t.Errorf("%q accepted", name)
		}
	}
}
