// SPDX-License-Identifier: Apache-2.0

// Package daemontest lets the protocols' tests stand a shell script in for
// their daemon and crash it. Only tests import it.
package daemontest

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Script is a stand-in daemon: it writes its pid to pidFile, exits on SIGTERM
// and otherwise runs until it is killed.
func Script(pidFile string) string {
	return "#!/bin/sh\necho $$ >" + pidFile + "\ntrap 'exit 0' TERM\nwhile :; do sleep 0.1; done\n"
}

// Pid waits for the stand-in daemon to write its pid to pidFile.
func Pid(t testing.TB, pidFile string) int {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		b, _ := os.ReadFile(pidFile) //nolint:gosec // G304: the test's own pid file
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid > 0 {
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatal("the stand-in daemon never wrote its pid")
		}
	}
}

// Gone fails t unless the process pid has ended, or is a zombie, within 5 s.
func Gone(t testing.TB, pid int, what string) {
	t.Helper()
	alive := func() bool {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return false
		}
		i := strings.LastIndexByte(string(b), ')') // the state follows the command name

		return i < 0 || i+2 >= len(b) || b[i+2] != 'Z'
	}
	for deadline := time.Now().Add(5 * time.Second); alive() && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if alive() {
		t.Errorf("the daemon still runs 5 s after %s", what)
	}
}

// Crash waits for the stand-in daemon to write its pid to pidFile, kills it as
// a crash would, and fails t unless done stays open until then and closes
// after.
func Crash(t testing.TB, pidFile string, done <-chan struct{}) {
	t.Helper()
	pid := Pid(t, pidFile)

	select {
	case <-done:
		t.Fatal("Done is closed while the daemon runs")
	default:
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	Closes(t, done, "the daemon died")
}

// Closes fails t unless done closes within 5 s of what happened.
func Closes(t testing.TB, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Done is still open 5 s after %s", what)
	}
}
