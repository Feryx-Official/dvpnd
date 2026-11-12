// SPDX-License-Identifier: Apache-2.0

package common

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Rules: [RT-4].
func TestEnsureForwarding(t *testing.T) {
	saved := forwardingSwitches
	t.Cleanup(func() { forwardingSwitches = saved })

	dir := t.TempDir()
	v4 := filepath.Join(dir, "ip_forward")
	v6 := filepath.Join(dir, "forwarding")
	forwardingSwitches = []forwardingSwitch{
		{v4, "net.ipv4.ip_forward", true},
		{v6, "net.ipv6.conf.all.forwarding", false},
	}

	read := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(b))
	}

	// Already on and not writable (a container started with --sysctl): no write, no error.
	if err := os.WriteFile(v4, []byte("1\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v6, []byte("1\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := EnsureForwarding(); err != nil {
		t.Fatalf("already enabled: %v", err)
	}

	// Off and writable (a bare host): turned on. Missing IPv6 file: warning only.
	if err := os.Chmod(v4, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v4, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(v6); err != nil {
		t.Fatal(err)
	}
	if err := EnsureForwarding(); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if got := read(v4); got != "1" {
		t.Fatalf("ip_forward = %q, want 1", got)
	}
}

// TestEnsureForwardingReadOnlyAndOff: a switch that is off and cannot be
// written (a container started without --sysctl) gives an error that says
// what to add. Root ignores file modes, so this case runs only as a user.
//
// Rules: [RT-4].
func TestEnsureForwardingReadOnlyAndOff(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	saved := forwardingSwitches
	t.Cleanup(func() { forwardingSwitches = saved })

	v4 := filepath.Join(t.TempDir(), "ip_forward")
	forwardingSwitches = []forwardingSwitch{{v4, "net.ipv4.ip_forward", true}}

	if err := os.WriteFile(v4, []byte("0\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	err := EnsureForwarding()
	if err == nil || !strings.Contains(err.Error(), "--sysctl net.ipv4.ip_forward=1") {
		t.Fatalf("read-only and off: err = %v", err)
	}
}
