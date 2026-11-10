// SPDX-License-Identifier: Apache-2.0

package amneziawg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
)

// startRig builds a service with both tiers, on interfaces awgtest0 and
// awgtest1, whose awg-quick is a script that logs its calls and fails for the
// interface named in failOn. The firewall commands go to the same log, and so
// does switching IP forwarding on, which the rig records instead of doing.
func startRig(t *testing.T, failOn string) (*AmneziaWG, func() []string) {
	t.Helper()

	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	record := func(line string) error {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteString(line + "\n")

		return err
	}

	quick := filepath.Join(dir, "awg-quick")
	script := "#!/bin/sh\necho \"awg-quick $*\" >> " + log + "\n[ \"$2\" = \"" + failOn + "\" ] && exit 1\nexit 0\n"
	if err := os.WriteFile(quick, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	savedQuick := Variant.Quick
	Variant.Quick = quick
	t.Cleanup(func() { Variant.Quick = savedQuick })

	saved, savedQuiet := common.RunCommand, common.RunQuiet
	common.RunCommand = func(name string, args ...string) error {
		if err := record(name + " " + strings.Join(args, " ")); err != nil {
			return err
		}
		if args[0] == "-C" { // no jump in place yet
			return os.ErrNotExist
		}
		return nil
	}
	common.RunQuiet = common.RunCommand
	t.Cleanup(func() { common.RunCommand, common.RunQuiet = saved, savedQuiet })

	savedForwarding := ensureForwarding
	ensureForwarding = func() error { return record("forwarding on") }
	t.Cleanup(func() { ensureForwarding = savedForwarding })

	_, cfg, s := setup(t) // after Variant.Quick is set: the service copies it
	cfg.Interface = "awgtest0"
	cfg.V3.Interface = "awgtest1"
	if err := s.Init(home(t, cfg)); err != nil {
		t.Fatal(err)
	}

	return s, func() []string {
		raw, _ := os.ReadFile(log)
		return strings.Split(strings.TrimSpace(string(raw)), "\n")
	}
}

func index(t *testing.T, calls []string, call string) int {
	t.Helper()

	for i, c := range calls {
		if c == call {
			return i
		}
	}
	t.Fatalf("no call %q in:\n%s", call, strings.Join(calls, "\n"))

	return -1
}

// firewallTools are the tools whose chains the egress firewall fills: IPv6's
// only on a host that has it.
func firewallTools() []string {
	tools := []string{"iptables"}
	if _, err := os.Stat("/proc/sys/net/ipv6"); err == nil {
		tools = append(tools, "ip6tables")
	}

	return tools
}

// TestStartFiltersBothTiersBeforeTheyComeUp: IP forwarding is switched on
// first; each tier's egress chains are filled and jumped to before its
// interface comes up, and taken away only after it is down.
//
// Rules: [EG-7], [RT-4].
func TestStartFiltersBothTiersBeforeTheyComeUp(t *testing.T) {
	s, calls := startRig(t, "")

	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}

	got := calls()
	if got[0] != "forwarding on" {
		t.Errorf("IP forwarding was not switched on first:\n%s", strings.Join(got, "\n"))
	}
	for _, iface := range []string{"awgtest0", "awgtest1"} {
		up := index(t, got, "awg-quick up "+iface)
		down := index(t, got, "awg-quick down "+iface)
		for _, tool := range firewallTools() {
			if i := index(t, got, tool+" -A DVPND-FWD-"+iface+" -d "+map[string]string{
				"iptables": "169.254.0.0/16", "ip6tables": "fe80::/10"}[tool]+" -j DROP"); i > up {
				t.Errorf("%s %s: the forward chain is filled after the interface is up", tool, iface)
			}
			if i := index(t, got, tool+" -I FORWARD 1 -i "+iface+" -j DVPND-FWD-"+iface); i > up {
				t.Errorf("%s %s: FORWARD jumps to the chain only after the interface is up", tool, iface)
			}
			if i := index(t, got, tool+" -I INPUT 1 -i "+iface+" -j DVPND-IN-"+iface); i > up {
				t.Errorf("%s %s: INPUT jumps to the chain only after the interface is up", tool, iface)
			}
			if i := index(t, got, tool+" -D FORWARD -i "+iface+" -j DVPND-FWD-"+iface); i < down {
				t.Errorf("%s %s: the jump is removed while the interface is still up", tool, iface)
			}
			index(t, got, tool+" -X DVPND-IN-"+iface)
		}
	}
}

// TestAFailedTierLeavesNothingBehind: when the 3.1 tier cannot come up, its
// chains come out, and the default tier, already up, is taken down before its
// own chains come out.
//
// Rules: [EG-7].
func TestAFailedTierLeavesNothingBehind(t *testing.T) {
	s, calls := startRig(t, "awgtest1")

	if err := s.Start(); err == nil {
		t.Fatal("Start succeeded although the 3.1 tier did not come up")
	}

	got := calls()
	if i := index(t, got, "iptables -X DVPND-FWD-awgtest1"); i < index(t, got, "awg-quick up awgtest1") {
		t.Error("the 3.1 tier's chains were removed before its attempt")
	}
	down := index(t, got, "awg-quick down awgtest0")
	for _, tool := range firewallTools() {
		if i := index(t, got, tool+" -X DVPND-FWD-awgtest0"); i < down {
			t.Errorf("%s: the default tier's chains were removed while it was still up", tool)
		}
		index(t, got, tool+" -X DVPND-IN-awgtest0")
		index(t, got, tool+" -X DVPND-IN-awgtest1")
	}
}
