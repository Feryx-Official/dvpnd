// SPDX-License-Identifier: Apache-2.0

package common

import (
	"os"
	"path/filepath"
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

// fakeTools puts scripts named after each tool first on PATH; each logs its
// name and arguments to the returned file.
func fakeTools(t *testing.T, names ...string) string {
	t.Helper()

	bin := t.TempDir()
	log := filepath.Join(bin, "calls.log")
	for _, name := range names {
		script := "#!/bin/sh\necho \"" + name + " $*\" >> " + log + "\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	return log
}

// TestIptablesWaitsForTheLock: iptables fails at once while another program
// (Docker, ufw, firewalld) holds the xtables lock unless told to wait; a
// removal that failed that way was dropped without a word.
//
// Rules: [RT-18].
func TestIptablesWaitsForTheLock(t *testing.T) {
	log := fakeTools(t, "iptables", "ip6tables", "wg")

	if err := RunCommand("iptables", "-A", "FORWARD", "-j", "X"); err != nil {
		t.Fatal(err)
	}
	if err := RunQuiet("ip6tables", "-D", "FORWARD", "-j", "X"); err != nil {
		t.Fatal(err)
	}
	if err := RunCommand("wg", "show"); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := "iptables -w 5 -A FORWARD -j X\nip6tables -w 5 -D FORWARD -j X\nwg show\n"
	if string(raw) != want {
		t.Fatalf("calls:\n%s\nwant:\n%s", raw, want)
	}
}
