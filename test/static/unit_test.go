// SPDX-License-Identifier: Apache-2.0

package static

import (
	"strconv"
	"strings"
	"testing"
)

// TestUnitLimitsWhatItsDaemonsMake holds scripts/dvpnd.service to the umask
// and the task cap it sets, the fast half of the systemd stage's
// TestUnitSandbox, which reads them from inside the unit. With systemd's
// default umask a file a daemon wrote without setting its mode was readable by
// every account on the host, and nothing bounded the tasks a daemon could
// start.
//
// Rules: [RT-29].
func TestUnitLimitsWhatItsDaemonsMake(t *testing.T) {
	const file = "scripts/dvpnd.service"
	settings := map[string][]string{}
	for _, line := range strings.Split(string(read(t, file)), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok && !strings.HasPrefix(key, "#") {
			settings[key] = append(settings[key], value)
		}
	}
	if len(settings["ExecStart"]) == 0 {
		t.Fatalf("%s has no ExecStart: this test reads the wrong file", file)
	}
	if got := settings["UMask"]; len(got) != 1 || got[0] != "0077" {
		t.Errorf("%s sets UMask %q, want 0077 once", file, got)
	}
	got := settings["TasksMax"]
	if len(got) != 1 {
		t.Fatalf("%s sets TasksMax %q, want one cap", file, got)
	}
	if n, err := strconv.Atoi(got[0]); err != nil || n <= 0 || n > 8192 {
		t.Errorf("%s caps the unit's tasks at %q, want a number from 1 to 8192", file, got[0])
	}
}
