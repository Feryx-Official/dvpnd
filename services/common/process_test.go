// SPDX-License-Identifier: Apache-2.0

package common

import (
	"errors"
	"os/exec"
	"testing"
	"time"
)

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
