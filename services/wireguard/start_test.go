// SPDX-License-Identifier: Apache-2.0

package wireguard

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
	wgtypes "github.com/Feryx-Official/dvpnd/v9/services/wireguard/types"
)

// startRig builds a service whose wg-quick is a script that logs its calls
// (and fails when told to), records the firewall commands in the same log,
// and leaves the kernel's forwarding switches alone.
func startRig(t *testing.T, iface string, quickFails bool) (*WireGuard, func() []string) {
	t.Helper()

	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	exit := "0"
	if quickFails {
		exit = "1"
	}
	quick := filepath.Join(dir, "wg-quick")
	script := "#!/bin/sh\necho \"wg-quick $*\" >> " + log + "\nexit " + exit + "\n"
	if err := os.WriteFile(quick, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	saved, savedQuiet := common.RunCommand, common.RunQuiet
	common.RunCommand = func(name string, args ...string) error {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, _ = f.WriteString(name + " " + strings.Join(args, " ") + "\n")
		if args[0] == "-C" { // no jump in place yet
			return os.ErrNotExist
		}
		return nil
	}
	common.RunQuiet = common.RunCommand
	t.Cleanup(func() { common.RunCommand, common.RunQuiet = saved, savedQuiet })

	savedForwarding := ensureForwarding
	ensureForwarding = func() error { return nil }
	t.Cleanup(func() { ensureForwarding = savedForwarding })

	cfg := wgtypes.NewConfig().WithDefaultValues()
	cfg.Interface = iface
	cfg.ListenPort = 51820
	cfg.Uplink = "eth0"
	key, err := wgtypes.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	cfg.PrivateKey = key.String()

	v4, _ := wgtypes.NewIPv4PoolFromCIDR("10.8.0.2/24")
	v6, _ := wgtypes.NewIPv6PoolFromCIDR("fd86:ea04:1115::2/120")
	variant := Default
	variant.Quick = quick
	variant.ConfigDir = dir
	s := NewVariant(variant, wgtypes.NewIPPool(v4, v6)).WithConfig(cfg, nil)
	if err := s.Init(dir); err != nil {
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

// TestStartFiltersBeforeTheInterfaceComesUp: the egress chains are filled
// and jumped to before wg-quick brings the interface up, and taken away only
// after it is down, so no peer packet passes unfiltered.
//
// Rules: [EG-7].
func TestStartFiltersBeforeTheInterfaceComesUp(t *testing.T) {
	s, calls := startRig(t, "wgtest0", false)

	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}

	got := calls()
	up := index(t, got, "wg-quick up wgtest0")
	down := index(t, got, "wg-quick down wgtest0")
	tools := []string{"iptables"}
	if _, err := os.Stat("/proc/sys/net/ipv6"); err == nil {
		tools = append(tools, "ip6tables")
	}
	for _, tool := range tools {
		if i := index(t, got, tool+" -A DVPND-FWD-wgtest0 -d "+map[string]string{
			"iptables": "169.254.0.0/16", "ip6tables": "fe80::/10"}[tool]+" -j DROP"); i > up {
			t.Errorf("%s: the forward chain is filled after the interface is up", tool)
		}
		if i := index(t, got, tool+" -I FORWARD 1 -i wgtest0 -j DVPND-FWD-wgtest0"); i > up {
			t.Errorf("%s: FORWARD jumps to the chain only after the interface is up", tool)
		}
		if i := index(t, got, tool+" -I INPUT 1 -i wgtest0 -j DVPND-IN-wgtest0"); i > up {
			t.Errorf("%s: INPUT jumps to the chain only after the interface is up", tool)
		}
		if i := index(t, got, tool+" -D FORWARD -i wgtest0 -j DVPND-FWD-wgtest0"); i < down {
			t.Errorf("%s: the jump is removed while the interface is still up", tool)
		}
		index(t, got, tool+" -X DVPND-IN-wgtest0")
	}
}

// TestFailedStartRemovesTheFirewall: when the interface cannot come up the
// chains do not outlive the attempt.
//
// Rules: [EG-7].
func TestFailedStartRemovesTheFirewall(t *testing.T) {
	s, calls := startRig(t, "wgtest1", true)

	if err := s.Start(); err == nil {
		t.Fatal("Start succeeded although wg-quick failed")
	}

	got := calls()
	if i := index(t, got, "iptables -X DVPND-FWD-wgtest1"); i < index(t, got, "wg-quick up wgtest1") {
		t.Fatal("chains removed before the attempt")
	}
}

// TestDetectedUplinkIsChecked: the name the default route gives goes through
// the same check as a configured one before it reaches PostUp.
//
// Rules: [RT-17].
func TestDetectedUplinkIsChecked(t *testing.T) {
	bin, dir := t.TempDir(), t.TempDir()
	ip := "#!/bin/sh\necho 'default via 192.0.2.1 dev eth0;reboot proto static'\n"
	if err := os.WriteFile(filepath.Join(bin, "ip"), []byte(ip), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := wgtypes.NewConfig().WithDefaultValues()
	cfg.Uplink = ""
	v4, _ := wgtypes.NewIPv4PoolFromCIDR("10.8.0.2/24")
	v6, _ := wgtypes.NewIPv6PoolFromCIDR("fd86:ea04:1115::2/120")
	variant := Default
	variant.ConfigDir = dir
	s := NewVariant(variant, wgtypes.NewIPPool(v4, v6)).WithConfig(cfg, nil)

	if err := s.Init(dir); err == nil {
		raw, _ := os.ReadFile(filepath.Join(dir, cfg.Interface+".conf"))
		t.Fatalf("Init took the uplink %q:\n%s", cfg.Uplink, raw)
	}
}

// TestAHungToolIsKilled: AddPeer and RemovePeer run under the admission
// lock and Peers in the usage pass; a wg that never returns is killed after
// the bound and the call fails, instead of holding every handshake. A hung
// wg-quick fails the start the same way.
//
// Rules: [RT-18].
func TestAHungToolIsKilled(t *testing.T) {
	s, _ := startRig(t, "wgtest2", false)
	hung := filepath.Join(t.TempDir(), "hung")
	if err := os.WriteFile(hung, []byte("#!/bin/sh\nexec sleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.variant.Tool, s.variant.Quick = hung, hung

	savedTool, savedQuick := common.ToolTimeout, quickTimeout
	common.ToolTimeout, quickTimeout = 200*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { common.ToolTimeout, quickTimeout = savedTool, savedQuick })

	key := make([]byte, 32)
	for name, call := range map[string]func() error{
		"AddPeer":    func() error { _, err := s.AddPeer(key); return err },
		"RemovePeer": func() error { return s.RemovePeer(key) },
		"Peers":      func() error { _, err := s.Peers(); return err },
		"Up":         s.Up,
	} {
		done := make(chan error, 1)
		go func() { done <- call() }()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s succeeded with a tool that never returned", name)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s still waits on a tool that never returns", name)
		}
	}
}

// TestKernelWithoutIPv6RunsIPv4Only: enable_ipv6 = true on a kernel booted
// without IPv6 runs the tunnel IPv4-only, as if the setting were off: no
// IPv6 address, no ip6tables in PostUp, no IPv6 tunnel address for a peer.
//
// Rules: [EG-11].
func TestKernelWithoutIPv6RunsIPv4Only(t *testing.T) {
	saved := kernelHasIPv6
	kernelHasIPv6 = func() bool { return false }
	t.Cleanup(func() { kernelHasIPv6 = saved })

	s, _ := startRig(t, "wgtest3", false)
	raw, err := os.ReadFile(filepath.Join(s.variant.ConfigDir, "wgtest3.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ip6tables") || strings.Contains(string(raw), "fd86:") {
		t.Errorf("IPv6 in the interface configuration:\n%s", raw)
	}

	tool := filepath.Join(t.TempDir(), "wg")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.variant.Tool = tool
	result, err := s.AddPeer(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	if !wgtypes.NewIPv6FromIP(result[4:]).Empty() {
		t.Errorf("the peer got an IPv6 tunnel address: %x", result[4:])
	}
}

// TestAForeignConfigurationIsLeftAlone: a file at the interface's path
// that the node did not write is never overwritten. One an earlier release
// wrote, without the marker but with the node's own private key, is taken
// over: rewritten with the marker, and readable by root only even when it
// was not before.
//
// Rules: [RT-19], [PV-6].
func TestAForeignConfigurationIsLeftAlone(t *testing.T) {
	init := func(t *testing.T, existing func(cfg *wgtypes.Config) string) (string, error) {
		dir := t.TempDir()
		cfg := wgtypes.NewConfig().WithDefaultValues()
		cfg.Uplink = "eth0"
		path := filepath.Join(dir, cfg.Interface+".conf")
		if err := os.WriteFile(path, []byte(existing(cfg)), 0o644); err != nil {
			t.Fatal(err)
		}
		v4, _ := wgtypes.NewIPv4PoolFromCIDR("10.8.0.2/24")
		v6, _ := wgtypes.NewIPv6PoolFromCIDR("fd86:ea04:1115::2/120")
		variant := Default
		variant.ConfigDir = dir

		return path, NewVariant(variant, wgtypes.NewIPPool(v4, v6)).WithConfig(cfg, nil).Init(dir)
	}

	other, _ := wgtypes.NewPrivateKey()
	foreign := "[Interface]\nAddress = 192.168.77.1/24\nPrivateKey = " + other.String() + "\nListenPort = 51820\n"
	path, err := init(t, func(*wgtypes.Config) string { return foreign })
	if err == nil {
		t.Error("Init took over a configuration it did not write")
	}
	if raw, _ := os.ReadFile(path); string(raw) != foreign {
		t.Errorf("the administrator's configuration was replaced:\n%s", raw)
	}

	path, err = init(t, func(cfg *wgtypes.Config) string {
		return "[Interface]\nAddress = 10.8.0.1/24\nListenPort = 51820\nPrivateKey = " + cfg.PrivateKey + "\n"
	})
	if err != nil {
		t.Fatalf("a configuration an earlier release wrote: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(raw), marker+"\n") {
		t.Errorf("rewritten without the marker:\n%s", raw)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600: %v", fi.Mode().Perm(), err)
	}

	if _, err = init(t, func(*wgtypes.Config) string { return string(raw) }); err != nil {
		t.Errorf("a configuration the node wrote: %v", err)
	}
}

// TestAForeignInterfaceIsNotTakenDown: when wg-quick cannot bring the
// interface up because it exists, the node tears it down and recreates it
// only when it is the node's own, left by a node that died: wg gives the
// node's private key for it.
//
// Rules: [RT-19].
func TestAForeignInterfaceIsNotTakenDown(t *testing.T) {
	for _, own := range []bool{false, true} {
		s, calls := startRig(t, "wgtest4", true)

		net := t.TempDir()
		if err := os.Mkdir(filepath.Join(net, "wgtest4"), 0o755); err != nil {
			t.Fatal(err)
		}
		saved := sysClassNet
		sysClassNet = net
		t.Cleanup(func() { sysClassNet = saved })

		key := s.config.PrivateKey
		if !own {
			other, _ := wgtypes.NewPrivateKey()
			key = other.String()
		}
		tool := filepath.Join(t.TempDir(), "wg")
		if err := os.WriteFile(tool, []byte("#!/bin/sh\n[ \"$*\" = \"show wgtest4 private-key\" ] && echo "+key+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		s.variant.Tool = tool

		_ = s.Up()
		down := slices.Contains(calls(), "wg-quick down wgtest4")
		if down != own {
			t.Errorf("own interface %v: taken down %v\n%s", own, down, strings.Join(calls(), "\n"))
		}
	}
}

// TestAPeerAddedTwiceKeepsItsAddresses: adding a peer that is already there
// hands back the addresses it holds; it took new ones before, which the
// interface then routed to it while the old ones stayed taken for good.
//
// Rules: [CT-9].
func TestAPeerAddedTwiceKeepsItsAddresses(t *testing.T) {
	s, _ := startRig(t, "wgtest5", false)
	tool := filepath.Join(t.TempDir(), "wg")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.variant.Tool = tool

	key := make([]byte, 32)
	first, err := s.AddPeer(key)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.AddPeer(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, again) || s.PeerCount() != 1 {
		t.Fatalf("addresses %x, then %x; %d peers", first, again, s.PeerCount())
	}

	if err := s.RemovePeer(key); err != nil {
		t.Fatal(err)
	}
	if v4, err := s.pool.V4.Get(); err != nil || !bytes.Equal(v4.Bytes(), first[:4]) {
		t.Fatalf("after the removal the pool hands out %v, %v; want %v", v4.IP(), err, net.IP(first[:4]))
	}
	if v4, err := s.pool.V4.Get(); err != nil || v4.IP().String() != "10.8.0.3" {
		t.Fatalf("an address was left taken: next is %v, %v", v4.IP(), err)
	}
}
