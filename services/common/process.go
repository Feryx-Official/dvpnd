// SPDX-License-Identifier: Apache-2.0

package common

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
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

// childEnv is a daemon's environment: the node's PATH, locale, time zone and
// temporary directory, the daemons' own tuning variables, and extra. The rest
// of what the operator or the container passed the node is not for a daemon
// that parses hostile traffic.
func childEnv(extra []string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if childEnvNames[name] || slices.ContainsFunc(childEnvPrefixes, func(p string) bool { return strings.HasPrefix(name, p) }) {
			env = append(env, kv)
		}
	}

	return append(env, extra...)
}

var (
	childEnvNames    = map[string]bool{"PATH": true, "TZ": true, "TMPDIR": true, "LANG": true}
	childEnvPrefixes = []string{"LC_", "XRAY_", "V2RAY_", "HYSTERIA_", "QUIC_GO_"}
)

// spawns runs each child's fork on one OS thread that lives as long as the
// node. The kernel sends a child its parent-death signal when the thread that
// forked it ends, not the process, and Go ends a thread whose goroutine exits
// while locked to it: started from any thread, a healthy daemon could be
// signalled, and the node would stop on its exit.
var (
	spawns       = make(chan func())
	spawnerStart sync.Once
)

func onSpawnerThread(f func()) {
	spawnerStart.Do(func() {
		go func() {
			runtime.LockOSThread() // never unlocked, and the goroutine never returns
			for f := range spawns {
				f()
			}
		}()
	})
	done := make(chan struct{})
	spawns <- func() {
		defer close(done)
		f()
	}
	<-done
}

// Process is a protocol's child process (a proxy or VPN daemon) with the
// lifecycle every service needs: start, reap on exit, stop politely.
type Process struct {
	cmd    *exec.Cmd
	done   chan struct{} // closed once the child has exited and been reaped
	err    error         // the child's Wait result, set before done is closed
	onStop func()        // runs once the child has exited after Stop
}

// StartProcess launches name with args; extraEnv is added to the environment
// childEnv keeps, and attr, when set, says which account the child runs as.
// The child's output goes to the node's stdout and stderr, and a goroutine
// reaps it whenever it exits so a crash leaves no zombie. A node killed
// outright runs no Stop, so the kernel sends the child SIGTERM when the node
// dies.
func StartProcess(name string, args []string, extraEnv []string, attr *syscall.SysProcAttr) (*Process, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = childEnv(extraEnv)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	if attr != nil {
		*cmd.SysProcAttr = *attr
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGTERM

	var err error
	onSpawnerThread(func() { err = cmd.Start() })
	if err != nil {
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
