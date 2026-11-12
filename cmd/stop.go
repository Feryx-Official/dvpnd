// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"os"
	"time"

	cmtlog "github.com/cometbft/cometbft/libs/log"

	"github.com/Feryx-Official/dvpnd/v9/types"
)

// daemonStartGrace is how long the start command watches a protocol daemon it
// has just started before it goes on: one that cannot run (a configuration it
// refuses, a port in use) exits within it, before the node goes active on the
// chain.
const daemonStartGrace = time.Second

// errDaemonExited ends a node whose protocol daemon is gone: it would stay
// listed as active, and take clients who paid for a session, while serving
// nothing. The non-zero exit has the service manager (systemd, Docker) start
// the node again.
var errDaemonExited = errors.New("the protocol daemon exited")

// stopper holds what ends a running node: a stop signal, the service's daemon
// exiting, or the node itself failing.
type stopper struct {
	log     cmtlog.Logger
	signals <-chan os.Signal
	daemon  <-chan struct{} // nil, never ready, for a service without one
	failed  <-chan error
}

// newStopper watches signals and, once service has started, its daemon.
func newStopper(log cmtlog.Logger, signals <-chan os.Signal, service types.Service) stopper {
	s := stopper{log: log, signals: signals}
	if d, ok := service.(types.Supervised); ok {
		s.daemon = d.Done()
	}

	return s
}

// within waits up to wait for a reason to stop, forever when wait is negative,
// and returns at once when one is already there. stop is false when none came
// in time; err is nil for a stop signal, a clean stop.
func (s stopper) within(wait time.Duration) (stop bool, err error) {
	select {
	case sig := <-s.signals:
		return s.onSignal(sig)
	case <-s.daemon:
		return s.onDaemonExit()
	case err := <-s.failed:
		return s.onFailure(err)
	default:
		if wait == 0 {
			return false, nil
		}
	}

	var timeout <-chan time.Time
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		timeout = timer.C
	}
	select {
	case sig := <-s.signals:
		return s.onSignal(sig)
	case <-s.daemon:
		return s.onDaemonExit()
	case err := <-s.failed:
		return s.onFailure(err)
	case <-timeout:
		return false, nil
	}
}

func (s stopper) onSignal(sig os.Signal) (bool, error) {
	s.log.Info("Stopping: signal received", "signal", sig.String())

	return true, nil
}

func (s stopper) onDaemonExit() (bool, error) {
	s.log.Error("Stopping: the protocol daemon exited; the service manager starts the node again")

	return true, errDaemonExited
}

func (s stopper) onFailure(err error) (bool, error) {
	s.log.Error("The node stopped on an error", "error", err)

	return true, err
}
