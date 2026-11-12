// SPDX-License-Identifier: Apache-2.0

package common

import (
	"errors"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
)

// verbose says whether the protocol daemons may log per-connection detail:
// client addresses and, for the proxies, the destinations clients reach. It
// follows the node's own log level, so a node at the default level keeps
// neither in its journal; an operator chasing a fault runs the node with
// --log_level debug and gets the daemons' usual output back.
var verbose atomic.Bool

// SetVerbose is called once by the start command, before the service writes
// its daemon's configuration.
func SetVerbose(v bool) { verbose.Store(v) }

// Verbose reports what SetVerbose stored.
func Verbose() bool { return verbose.Load() }

// Process is a protocol's child process (a proxy or VPN daemon) with the
// lifecycle every service needs: start, reap on exit, stop politely.
type Process struct {
	cmd    *exec.Cmd
	done   chan struct{} // closed once the child has exited and been reaped
	err    error         // the child's Wait result, set before done is closed
	onStop func()        // runs once the child has exited after Stop
}

// StartProcess launches name with args; extraEnv is appended to the current
// environment and attr, when set, says which account the child runs as. The
// child's output goes to the node's stdout and stderr, and a goroutine reaps
// it whenever it exits so a crash leaves no zombie.
func StartProcess(name string, args []string, extraEnv []string, attr *syscall.SysProcAttr) (*Process, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = attr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	p := &Process{cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()

	return p, nil
}

// Done is closed once the child has exited, on its own or through Stop. It is
// nil, so never ready, for a process that was not started.
func (p *Process) Done() <-chan struct{} {
	if p == nil {
		return nil
	}

	return p.done
}

// EitherDone returns a channel closed once a or b is closed: a daemon and the
// node's channel to it, either of which gone leaves the service unable to serve.
func EitherDone(a, b <-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		select {
		case <-a:
		case <-b:
		}
		close(done)
	}()

	return done
}

// Err is how the child exited, once Done is closed.
func (p *Process) Err() error {
	<-p.Done()

	return p.err
}

// Stop sends SIGTERM and waits for the child, killing it after timeout. An
// exit status reported after the signal is expected and not an error.
func (p *Process) Stop(timeout time.Duration) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return errors.New("process was not started")
	}
	if p.onStop != nil {
		defer p.onStop()
	}

	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}

	select {
	case <-p.done:
		return nil
	case <-time.After(timeout):
	}

	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	<-p.done

	return nil
}

// Exited reports whether the child has exited, and how.
func (p *Process) Exited() (bool, *os.ProcessState) {
	if p == nil || p.cmd == nil || p.cmd.ProcessState == nil {
		return false, nil
	}

	return true, p.cmd.ProcessState
}
