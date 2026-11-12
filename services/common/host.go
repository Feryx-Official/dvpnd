// SPDX-License-Identifier: Apache-2.0

package common

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

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

		err := os.WriteFile(sw.path, []byte("1\n"), 0o644)
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
	if out, err := exec.Command("ip", "-o", "-4", "route", "show", "default").Output(); err == nil {
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
