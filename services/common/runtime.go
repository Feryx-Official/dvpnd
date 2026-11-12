// SPDX-License-Identifier: Apache-2.0

package common

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// ProxyUserName is the system account the proxy daemons (V2Ray, XRAY,
// Hysteria2) run as, and OpenVPN drops to once its tunnel is up. The
// installer and the Docker image create it. A bug in a daemon that parses
// hostile traffic then gets an account that can read nothing of the node's
// and whose traffic the kernel holds to the egress policy (ProxyEgress),
// rather than root and the wallet key.
const ProxyUserName = "dvpnd-proxy"

// Runtime is where the node writes what the protocol daemons read at start
// (their configuration, a copy of the TLS key) and which account the
// daemons run as.
type Runtime struct {
	// Dir holds the daemons' runtime files.
	Dir string
	// Proxy is the account the daemons run as; nil when they run as the
	// node does (the node is not root, or the account does not exist).
	Proxy *ProxyUser
}

// ProxyUser is the proxy account.
type ProxyUser struct {
	Name, Group string
	UID, GID    uint32
}

var runtimeState struct {
	sync.RWMutex
	rt  Runtime
	set bool
}

// SetRuntime is called once by the start command, before the service's Init.
func SetRuntime(rt Runtime) {
	runtimeState.Lock()
	defer runtimeState.Unlock()
	runtimeState.rt, runtimeState.set = rt, true
}

// CurrentRuntime is what SetRuntime stored; until it is called (in tests)
// the daemons run as the node does and their files go to the temporary
// directory.
func CurrentRuntime() Runtime {
	runtimeState.RLock()
	defer runtimeState.RUnlock()
	if !runtimeState.set {
		return Runtime{Dir: os.TempDir()}
	}

	return runtimeState.rt
}

// lookupProxyUser finds the proxy account; a variable so tests can fake it.
var lookupProxyUser = func() (*ProxyUser, error) {
	u, err := user.Lookup(ProxyUserName)
	if err != nil {
		return nil, err
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, err
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return nil, err
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		return nil, err
	}

	return &ProxyUser{Name: u.Username, Group: g.Name, UID: uint32(uid), GID: uint32(gid)}, nil
}

// PrepareRuntime picks the runtime directory and the daemons' account and
// creates the directory. daemon says whether the service runs a daemon that
// can drop to the proxy account; for the others the account is left out.
// Under systemd the directory is the unit's RuntimeDirectory; a node running
// as root otherwise uses /run/dvpnd, since the proxy account cannot enter
// root's home; any other node uses run/ in its home. The directory is
// readable by the proxy account's group only.
func PrepareRuntime(home string, euid int, daemon bool) (Runtime, error) {
	var rt Runtime
	if euid == 0 && daemon {
		if u, err := lookupProxyUser(); err == nil {
			rt.Proxy = u
		}
	}

	switch dirs := os.Getenv("RUNTIME_DIRECTORY"); {
	case dirs != "":
		rt.Dir, _, _ = strings.Cut(dirs, ":")
	case euid == 0:
		rt.Dir = "/run/dvpnd"
	default:
		rt.Dir = filepath.Join(home, "run")
	}

	if err := os.MkdirAll(rt.Dir, 0o700); err != nil {
		return rt, err
	}
	if rt.Proxy == nil {
		return rt, os.Chmod(rt.Dir, 0o700)
	}
	if err := os.Chown(rt.Dir, 0, int(rt.Proxy.GID)); err != nil {
		return rt, fmt.Errorf("runtime directory %s: %w", rt.Dir, err)
	}

	return rt, os.Chmod(rt.Dir, 0o750)
}

// WriteFile writes a file the daemon reads into the runtime directory:
// readable by the proxy account's group when the daemon runs as it, by the
// owner only otherwise.
func (rt Runtime) WriteFile(name string, data []byte) (string, error) {
	path := filepath.Join(rt.Dir, name)
	mode := os.FileMode(0o600)
	if rt.Proxy != nil {
		mode = 0o640
	}

	written.Lock()
	if written.paths == nil {
		written.paths = map[string]bool{}
	}
	written.paths[path] = true
	written.Unlock()

	if err := os.WriteFile(path, data, mode); err != nil {
		return "", err
	}
	if rt.Proxy != nil {
		if err := os.Chown(path, 0, int(rt.Proxy.GID)); err != nil {
			return "", err
		}
	}

	return path, os.Chmod(path, mode)
}

// TLSFiles copies the node's certificate and key from its home, which the
// proxy account cannot read, into the runtime directory, and returns the
// copies' paths.
func (rt Runtime) TLSFiles(home string) (cert, key string, err error) {
	for _, f := range []struct {
		name string
		path *string
	}{{"tls.crt", &cert}, {"tls.key", &key}} {
		data, err := os.ReadFile(filepath.Join(home, f.name))
		if err != nil {
			return "", "", err
		}
		if *f.path, err = rt.WriteFile(f.name, data); err != nil {
			return "", "", err
		}
	}

	return cert, key, nil
}

// capNetBindService is CAP_NET_BIND_SERVICE.
const capNetBindService = 10

// proxyAttr is how a proxy daemon is started: as the proxy account when the
// node has one, keeping the right to bind a port below 1024 if the daemon
// listens on one.
func (rt Runtime) proxyAttr(listenPort uint16) *syscall.SysProcAttr {
	if rt.Proxy == nil {
		return nil
	}

	attr := &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: rt.Proxy.UID, Gid: rt.Proxy.GID, Groups: []uint32{}},
	}
	if listenPort < 1024 {
		attr.AmbientCaps = []uintptr{capNetBindService}
	}

	return attr
}

// StartProxy starts a proxy daemon: as the proxy account behind its kernel
// egress chain when the node has one, as the node otherwise. loopback are
// the ports on 127.0.0.1 the daemon must reach. Stopping the process removes
// the chain.
func StartProxy(name string, args, extraEnv []string, listenPort uint16, loopback ...uint16) (*Process, error) {
	rt := CurrentRuntime()
	if rt.Proxy == nil {
		return StartProcess(name, args, extraEnv, nil)
	}

	egress := ProxyEgress{UID: rt.Proxy.UID, LoopbackPorts: loopback}
	if err := egress.Up(); err != nil {
		egress.Down()
		return nil, err
	}

	p, err := StartProcess(name, args, extraEnv, rt.proxyAttr(listenPort))
	if err != nil {
		egress.Down()
		return nil, err
	}
	p.onStop = egress.Down

	return p, nil
}

// written holds the paths WriteFile wrote, for RemoveFiles.
var written struct {
	sync.Mutex
	paths map[string]bool
}

// RemoveFiles removes the files WriteFile wrote into the directory: a node
// stops its daemons first, and a REALITY private key, the stats secret or a
// copy of the TLS key has no reason to stay on disk.
func (rt Runtime) RemoveFiles() error {
	written.Lock()
	defer written.Unlock()

	var errs []error
	for path := range written.paths {
		if filepath.Dir(path) != filepath.Clean(rt.Dir) {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
			continue
		}
		delete(written.paths, path)
	}

	return errors.Join(errs...)
}
