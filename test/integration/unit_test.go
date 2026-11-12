// SPDX-License-Identifier: Apache-2.0

//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUnitSandbox holds scripts/dvpnd.service to the limits it sets on the
// node and its daemons, read from inside the unit as shipped (the systemd
// stage). A file or directory is its owner's only unless its mode is set
// after it is made: with systemd's default umask, 0022, any account on the
// host could read what a daemon wrote. The unit's tasks are capped, so a daemon that
// forks or starts threads without bound cannot use up the host's.
//
// Rules: [RT-29].
func TestUnitSandbox(t *testing.T) {
	requireEnv(t)
	if os.Getenv("DVPND_IT_UNIT") == "" {
		t.Skip("runs under the systemd unit only (the systemd stage)")
	}

	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		if umask, ok := strings.CutPrefix(line, "Umask:"); ok && strings.TrimSpace(umask) != "0077" {
			t.Errorf("the unit runs the node with umask %s, want 0077", strings.TrimSpace(umask))
		}
	}
	path := filepath.Join(t.TempDir(), "made-without-a-mode")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("a file written with mode 0644 under the unit has mode %v, want 0600", info.Mode().Perm())
	}

	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	group := ""
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			group = rest
		}
	}
	if !strings.HasSuffix(group, "/dvpnd.service") {
		t.Fatalf("the test runs in cgroup %q, not the unit's", group)
	}
	limit, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", group, "pids.max"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(limit)); got != "4096" {
		t.Errorf("the unit's tasks are capped at %s, want 4096", got)
	}
}
