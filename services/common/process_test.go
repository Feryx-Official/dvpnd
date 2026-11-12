// SPDX-License-Identifier: Apache-2.0

package common

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alive tells whether the process pid is still there and not a zombie.
func alive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	i := bytes.LastIndexByte(b, ')') // the state follows the command name

	return i < 0 || i+2 >= len(b) || b[i+2] != 'Z'
}

// waitForPid reads the pid a stand-in daemon writes to file.
func waitForPid(t *testing.T, file string) int {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		b, _ := os.ReadFile(file)
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid > 0 {
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatal("the stand-in daemon never wrote its pid")
		}
	}
}

// TestChildEndsWithAKilledNode: a node killed outright (SIGKILL, the OOM
// killer) runs no Stop, and outside systemd or Docker nothing else ends its
// daemon; the kernel now sends the daemon SIGTERM. A copy of this test binary
// is the node here.
//
// Rules: [RT-3].
func TestChildEndsWithAKilledNode(t *testing.T) {
	if pidFile := os.Getenv("DVPND_TEST_NODE_OF"); pidFile != "" {
		script := "echo $$ >" + pidFile + "; trap 'exit 0' TERM; while :; do sleep 0.05; done"
		if _, err := StartProcess("sh", []string{"-c", script}, nil, nil); err != nil {
			os.Exit(2)
		}
		time.Sleep(time.Hour)
	}

	pidFile := filepath.Join(t.TempDir(), "pid")
	node := exec.Command(os.Args[0], "-test.run=^TestChildEndsWithAKilledNode$")
	node.Env = append(os.Environ(), "DVPND_TEST_NODE_OF="+pidFile)
	if err := node.Start(); err != nil {
		t.Fatal(err)
	}
	daemon := waitForPid(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(daemon, syscall.SIGKILL) })

	_ = node.Process.Kill()
	_ = node.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for alive(daemon) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(daemon) {
		t.Error("the daemon still runs 5 s after its node was killed")
	}
}

// TestChildGetsOnlyTheEnvironmentItRunsOn: a daemon parses hostile traffic,
// so it does not get whatever the operator or the container passed the node:
// only the PATH, locale, time zone and temporary directory, the daemons' own
// tuning variables, and what its service adds.
//
// Rules: [PV-10].
func TestChildGetsOnlyTheEnvironmentItRunsOn(t *testing.T) {
	t.Setenv("DVPND_TEST_SECRET", "not for the daemon")
	t.Setenv("XRAY_TEST_TUNING", "1")
	t.Setenv("TZ", "UTC")
	out := filepath.Join(t.TempDir(), "env")
	p, err := StartProcess("sh", []string{"-c", "env >" + out}, []string{"FROM_SERVICE=1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Err(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	env := "\n" + string(b)
	for _, want := range []string{"\nPATH=", "\nTZ=UTC\n", "\nXRAY_TEST_TUNING=1\n", "\nFROM_SERVICE=1\n"} {
		if !strings.Contains(env, want) {
			t.Errorf("the daemon's environment lacks %q", strings.TrimSpace(want))
		}
	}
	if strings.Contains(env, "DVPND_TEST_SECRET") {
		t.Error("the daemon got a variable of the node's that is not its to read")
	}
}

// TestDoneWhenTheChildExits: a daemon that exits on its own closes Done and
// says how it exited. The start command stops the node on it: a node whose
// daemon is gone serves nothing while it stays active on the chain.
//
// Rules: [RT-15].
func TestDoneWhenTheChildExits(t *testing.T) {
	p, err := StartProcess("sh", []string{"-c", "sleep 0.3; exit 3"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
		t.Fatal("Done is closed while the child still runs")
	default:
	}

	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done is still open 5 s after the child exited")
	}
	var exit *exec.ExitError
	if err := p.Err(); !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("Err() = %v, want the child's exit status 3", err)
	}
	if err := p.Stop(time.Second); err != nil {
		t.Fatalf("Stop after the child exited: %v", err)
	}
}

// TestDoneAfterStop: Stop leaves Done closed, and a process that never started
// has a Done that never closes.
//
// Rules: [RT-3].
func TestDoneAfterStop(t *testing.T) {
	p, err := StartProcess("sh", []string{"-c", "trap 'exit 0' TERM; while :; do sleep 0.05; done"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("Done is open after Stop returned")
	}

	var none *Process
	if none.Done() != nil {
		t.Fatal("a process that never started has a Done channel")
	}
}
