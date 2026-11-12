// SPDX-License-Identifier: Apache-2.0

package common

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// The tunnel services share what they need from the host: IP forwarding and
// the interface peer traffic leaves through.

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
func DetectUplink() string {
	out, err := exec.Command("ip", "-o", "-4", "route", "show", "default").Output()
	if err == nil {
		fields := strings.Fields(string(out))
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "dev" {
				return fields[i+1]
			}
		}
	}

	return "eth0"
}
