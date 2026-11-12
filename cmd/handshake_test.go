// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cmtlog "github.com/cometbft/cometbft/libs/log"

	"github.com/Feryx-Official/dvpnd/v9/services/common/daemontest"
)

// stubHnsd puts script where the hnsd binary is looked for.
func stubHnsd(t *testing.T, script string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hnsd")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	saved := hnsdBinary
	hnsdBinary = path
	t.Cleanup(func() { hnsdBinary = saved })
}

// stopWithin fails t unless r.Stop returns within 10 s.
func stopWithin(t *testing.T, r *resolver) {
	t.Helper()
	stopped := make(chan struct{})
	go func() {
		r.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the resolver's Stop has not returned after 10 s")
	}
}

// TestHandshakeResolverStopsWithTheNode: the Handshake resolver is started
// like the protocol daemons and stopped with the node. It ran as root through
// exec.Command, and nothing stopped it.
//
// Rules: [RT-1].
func TestHandshakeResolverStopsWithTheNode(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	stubHnsd(t, daemontest.Script(pidFile))

	r := startHandshake(cmtlog.NewNopLogger(), 8, net.IPv4(10, 8, 0, 1))
	pid := daemontest.Pid(t, pidFile)
	stopWithin(t, r)
	daemontest.Gone(t, pid, "the node stopped the resolver")
}

// TestHandshakeResolverIsStartedAgain: a resolver that exits is started again
// after the restart delay, and the node's stop ends the wait.
//
// Rules: [RT-1].
func TestHandshakeResolverIsStartedAgain(t *testing.T) {
	runs := filepath.Join(t.TempDir(), "runs")
	stubHnsd(t, "#!/bin/sh\necho run >>"+runs+"\nexit 1\n")
	saved := handshakeRestartDelay
	handshakeRestartDelay = 50 * time.Millisecond
	t.Cleanup(func() { handshakeRestartDelay = saved })

	waitRuns := func(n int) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			if b, _ := os.ReadFile(runs); strings.Count(string(b), "run") >= n {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("the resolver did not run %d times within 5 s", n)
			}
		}
	}

	r := startHandshake(cmtlog.NewNopLogger(), 8, net.IPv4(10, 8, 0, 1))
	waitRuns(2)
	stopWithin(t, r)

	// The node's stop ends the wait before a restart.
	handshakeRestartDelay = time.Hour
	_ = os.Remove(runs)
	r = startHandshake(cmtlog.NewNopLogger(), 8, net.IPv4(10, 8, 0, 1))
	waitRuns(1)
	stopWithin(t, r)
}
