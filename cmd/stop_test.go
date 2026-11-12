// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	cmtlog "github.com/cometbft/cometbft/libs/log"

	"github.com/Feryx-Official/dvpnd/v9/types"
)

// daemonService is a service with a protocol daemon; only Done is called.
type daemonService struct {
	types.Service
	done chan struct{}
}

func (s daemonService) Done() <-chan struct{} { return s.done }

// TestStopperStopsWhenTheDaemonExits: a node whose protocol daemon exits
// stops, with an error so its service manager starts it again. It stayed up
// and active on the chain, serving nothing. A service without a daemon never
// stops the node that way.
//
// Rules: [RT-15].
func TestStopperStopsWhenTheDaemonExits(t *testing.T) {
	log := cmtlog.NewNopLogger()
	signals := make(chan os.Signal, 1)

	service := daemonService{done: make(chan struct{})}
	stop := newStopper(log, signals, service)
	if halt, err := stop.within(50 * time.Millisecond); halt {
		t.Fatalf("stopped while the daemon runs: %v", err)
	}
	close(service.done)
	for _, wait := range []time.Duration{0, time.Second} {
		if halt, err := stop.within(wait); !halt || !errors.Is(err, errDaemonExited) {
			t.Errorf("within(%s) once the daemon exited: stop %v, error %v; want a stop with %v", wait, halt, err, errDaemonExited)
		}
	}
	waited := make(chan error, 1)
	go func() { _, err := stop.within(-1); waited <- err }()
	select {
	case err := <-waited:
		if !errors.Is(err, errDaemonExited) {
			t.Errorf("the node's wait once the daemon exited ended with %v, want %v", err, errDaemonExited)
		}
	case <-time.After(5 * time.Second):
		t.Error("the node's wait goes on 5 s after the daemon exited")
	}

	var plain struct{ types.Service }
	if halt, err := newStopper(log, signals, plain).within(50 * time.Millisecond); halt {
		t.Errorf("a service without a daemon stopped the node: %v", err)
	}
}

// TestStopperStopsOnASignalOrAFailure: a stop signal is a clean stop, a failed
// node stops with its error, and a reason already there is seen without
// waiting.
//
// Rules: [RT-2].
func TestStopperStopsOnASignalOrAFailure(t *testing.T) {
	signals := make(chan os.Signal, 1)
	failed := make(chan error, 1)
	stop := stopper{log: cmtlog.NewNopLogger(), signals: signals, failed: failed}

	if halt, err := stop.within(0); halt {
		t.Fatalf("stopped with no reason: %v", err)
	}
	signals <- syscall.SIGTERM
	if halt, err := stop.within(0); !halt || err != nil {
		t.Errorf("a pending SIGTERM: stop %v, error %v; want a clean stop", halt, err)
	}
	failure := errors.New("the API server failed")
	failed <- failure
	if halt, err := stop.within(-1); !halt || !errors.Is(err, failure) {
		t.Errorf("a failed node: stop %v, error %v; want a stop with its error", halt, err)
	}

	started := time.Now()
	if halt, err := stop.within(100 * time.Millisecond); halt || time.Since(started) < 100*time.Millisecond {
		t.Errorf("within(100ms) with no reason: stop %v (%v) after %s", halt, err, time.Since(started))
	}
}
