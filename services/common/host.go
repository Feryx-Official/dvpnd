// SPDX-License-Identifier: Apache-2.0

package common

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// ToolTimeout bounds one call of a tool the node runs on the host: iptables,
// ip, wg, awg. AddPeer runs wg under the admission lock, so a tool that hung
// held every handshake, and a hung iptables held the start.
var ToolTimeout = 15 * time.Second

// xtablesWait is how many seconds iptables and ip6tables wait for the
// xtables lock while another program (Docker, ufw, firewalld) holds it;
// without it they fail at once.
const xtablesWait = "5"

// toolCommand is the command for a tool the node runs on the host. Once ctx
// is done the tool gets SIGTERM, so wg-quick can undo what it began, and
// SIGKILL if it is still there a few seconds later.
func toolCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	if name == "iptables" || name == "ip6tables" {
		args = append([]string{"-w", xtablesWait}, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: an argv call, no shell, to the node's own tools
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second

	return cmd
}

// RunTool runs a tool on the host with its output going to the node's own,
// and stops it once timeout has passed.
func RunTool(timeout time.Duration, name string, args ...string) error {
	return runTool(timeout, os.Stdout, os.Stderr, name, args...)
}

func runTool(timeout time.Duration, stdout, stderr io.Writer, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := toolCommand(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = stdout, stderr

	return toolError(ctx, name, timeout, cmd.Run())
}

// ToolOutput runs a tool on the host and returns what it printed; it is
// stopped once timeout has passed.
func ToolOutput(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	out, err := toolCommand(ctx, name, args...).Output()

	return out, toolError(ctx, name, timeout, err)
}

// toolError says so when a tool failed because it ran past its bound.
func toolError(ctx context.Context, name string, timeout time.Duration, err error) error {
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("%s did not finish within %s: %w", name, timeout, err)
	}

	return err
}

// The tunnel services share what they need from the host: IP forwarding,
// the interface peer traffic leaves through, and the check every interface
// name passes.

// interfaceName is what an interface or uplink setting may hold: letters,
// digits, '_', '.' and '-', starting with a letter or digit so iptables
// cannot read it as an option, at most 15 characters (the kernel's limit).
var interfaceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,14}$`)

// CheckInterfaceName refuses a name that is not a plain interface name. The
// names go into iptables commands, the daemons' configurations and the
// PostUp line wg-quick runs in a root shell, so shell syntax in one would
// run as root.
func CheckInterfaceName(setting, name string) error {
	if !interfaceName.MatchString(name) {
		return fmt.Errorf("%s %q is not an interface name: use up to 15 letters, digits, '_', '.' or '-'", setting, name)
	}

	return nil
}

// forwardingSwitch is a kernel setting peer traffic depends on. IPv4 is
// mandatory; IPv6 is best-effort because a host or container without IPv6
// has no such file.
type forwardingSwitch struct {
	path     string
	name     string
	required bool
}

var forwardingSwitches = []forwardingSwitch{
	{"/proc/sys/net/ipv4/ip_forward", "net.ipv4.ip_forward", true},
	{"/proc/sys/net/ipv6/conf/all/forwarding", "net.ipv6.conf.all.forwarding", false},
}

// EnsureForwarding turns IP forwarding on unless it already is. It reads before
// writing because /proc/sys is read-only inside an unprivileged container: there
// the value must come from the runtime (docker run --sysctl net.ipv4.ip_forward=1)
// and a blind write would fail even though the setting is right.
func EnsureForwarding() error {
	for _, sw := range forwardingSwitches {
		if cur, err := os.ReadFile(sw.path); err == nil && strings.TrimSpace(string(cur)) == "1" {
			continue
		}

		err := os.WriteFile(sw.path, []byte("1\n"), 0o644) //nolint:gosec // G306: a /proc/sys switch, which exists, so the mode is never used
		if err == nil {
			continue
		}
		if sw.required {
			return fmt.Errorf("%s is off and cannot be enabled (%v); set it on the host, or pass --sysctl %s=1 to the container runtime", sw.name, err, sw.name)
		}

		fmt.Fprintf(os.Stderr, "warning: %s could not be enabled (%v); IPv6 peer traffic will not be forwarded\n", sw.name, err)
	}

	return nil
}

// DetectUplink returns the interface of the default IPv4 route, which is where
// peer traffic must be masqueraded. Falls back to eth0, upstream's fixed choice.
// The name goes through the same check as a configured one.
func DetectUplink() (string, error) {
	name := "eth0"
	if out, err := ToolOutput(ToolTimeout, "ip", "-o", "-4", "route", "show", "default"); err == nil {
		fields := strings.Fields(string(out))
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "dev" {
				name = fields[i+1]
				break
			}
		}
	}
	if err := CheckInterfaceName("the default route's interface", name); err != nil {
		return "", fmt.Errorf("%w; set uplink in the service's configuration", err)
	}

	return name, nil
}
