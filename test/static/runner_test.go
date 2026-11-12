// SPDX-License-Identifier: Apache-2.0

package static

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeLogger is a command that logs its name and arguments to $LOG and
// succeeds, standing for a tool that changes the host.
const fakeLogger = `#!/bin/sh
echo "$(basename "$0") $*" >>"$LOG"
`

// fakeOpenSSL writes the key and the certificate where openssl req is told to.
const fakeOpenSSL = `#!/bin/sh
echo "openssl $*" >>"$LOG"
while [ $# -gt 0 ]; do
  case "$1" in
    -keyout) echo "new key" >"$2"; shift ;;
    -out) echo "new certificate" >"$2"; shift ;;
  esac
  shift
done
`

// runnerHost is a host for the Docker runner: a PATH of the tools it may
// use, fakes for those that change the host, and a home and a daemon.json of
// the test's own.
type runnerHost struct {
	dir, bin, home, daemonJSON, log string
}

// newRunnerHost makes a runnerHost; with docker false, Docker is not
// installed. Tools from extra are linked from this machine.
func newRunnerHost(t *testing.T, docker bool, fakes map[string]string, extra ...string) *runnerHost {
	t.Helper()
	dir := t.TempDir()
	h := &runnerHost{dir: dir, bin: filepath.Join(dir, "bin"), home: filepath.Join(dir, "root"),
		daemonJSON: filepath.Join(dir, "etc", "docker", "daemon.json"), log: filepath.Join(dir, "log")}
	for _, d := range []string{h.bin, h.home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range append([]string{"bash", "sh", "cat", "mkdir", "dirname", "basename", "chmod", "rm", "grep", "sed", "awk"}, extra...) {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("%s is not installed here: %v", tool, err)
		}
		if err := os.Symlink(path, filepath.Join(h.bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	all := map[string]string{"apt-get": fakeLogger, "systemctl": fakeLogger, "curl": fakeLogger, "openssl": fakeOpenSSL}
	if docker {
		all["docker"] = fakeLogger
	}
	for name, script := range fakes {
		all[name] = script
	}
	for name, script := range all {
		if err := os.WriteFile(filepath.Join(h.bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	return h
}

// run sources the runner on the host and runs script, returning its output
// and whether it succeeded.
func (h *runnerHost) run(t *testing.T, script string) (string, bool) {
	t.Helper()
	runner := filepath.Join(root(t), "scripts", "runner.sh")
	cmd := exec.Command(filepath.Join(h.bin, "bash"), "-c", "source "+runner+"\nDOCKER_DAEMON_CONFIG="+h.daemonJSON+"\n"+script)
	cmd.Env = []string{"PATH=" + h.bin, "HOME=" + h.home, "LOG=" + h.log}
	out, err := cmd.CombinedOutput()

	return string(out), err == nil
}

// calls returns what the fakes logged.
func (h *runnerHost) calls(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(h.log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	return string(b)
}

// TestRunnerSetupKeepsTheHostsSettings holds the Docker runner's setup to the
// host it is given. It ran get.docker.com's script as root, unchecked; it
// rewrote /etc/docker/daemon.json, dropping the host's own settings, and
// restarted Docker, and every container with it; it installed
// iptables-persistent, which removes ufw; and it replaced the TLS certificate
// clients pin at every run. setup_host runs here on a host of the test's own,
// with fakes for the tools that change it.
//
// Rules: [RT-27].
func TestRunnerSetupKeepsTheHostsSettings(t *testing.T) {
	refused := func(calls string) {
		t.Helper()
		for _, call := range strings.Split(calls, "\n") {
			if strings.HasPrefix(call, "curl ") || strings.Contains(call, "iptables-persistent") || strings.Contains(call, "get.docker.com") {
				t.Errorf("setup ran %q on the host", call)
			}
		}
	}

	t.Run("a host without Docker", func(t *testing.T) {
		h := newRunnerHost(t, false, nil)
		out, ok := h.run(t, "setup_host")
		if ok {
			t.Errorf("setup succeeded without Docker:\n%s", out)
		}
		if !strings.Contains(out, "https://docs.docker.com/engine/install/") {
			t.Errorf("setup did not say where to install Docker from:\n%s", out)
		}
		refused(h.calls(t))
		if _, err := os.Stat(h.daemonJSON); !os.IsNotExist(err) {
			t.Errorf("setup wrote %s on a host without Docker", h.daemonJSON)
		}
	})

	t.Run("a host with its own settings", func(t *testing.T) {
		h := newRunnerHost(t, true, nil)
		own := map[string]string{h.daemonJSON: `{"log-driver": "local"}` + "\n",
			filepath.Join(h.home, ".dvpnd", "tls.crt"): "the certificate clients pin\n",
			filepath.Join(h.home, ".dvpnd", "tls.key"): "its key\n"}
		for path, content := range own {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		out, ok := h.run(t, "setup_host")
		if !ok {
			t.Fatalf("setup failed:\n%s", out)
		}
		for path, content := range own {
			if b, _ := os.ReadFile(path); string(b) != content {
				t.Errorf("setup replaced %s:\n%s", path, b)
			}
		}
		calls := h.calls(t)
		refused(calls)
		if strings.Contains(calls, "systemctl restart docker") {
			t.Errorf("setup restarted Docker, and every container, on a host whose daemon.json it kept:\n%s", calls)
		}
		if !strings.Contains(out, "does not give containers IPv6") {
			t.Errorf("setup did not say the host's daemon.json leaves containers without IPv6:\n%s", out)
		}
	})

	t.Run("a fresh host", func(t *testing.T) {
		h := newRunnerHost(t, true, nil)
		out, ok := h.run(t, "setup_host")
		if !ok {
			t.Fatalf("setup failed:\n%s", out)
		}
		b, err := os.ReadFile(h.daemonJSON)
		if err != nil || !strings.Contains(string(b), `"ipv6": true`) || !strings.Contains(string(b), `"ip6tables": true`) {
			t.Errorf("setup did not give containers IPv6 as the guide does: %v\n%s", err, b)
		}
		calls := h.calls(t)
		refused(calls)
		if !strings.Contains(calls, "systemctl restart docker") {
			t.Errorf("setup wrote daemon.json without restarting Docker:\n%s", calls)
		}
		if info, err := os.Stat(filepath.Join(h.home, ".dvpnd", "tls.key")); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("setup made no TLS key readable by root only: %v", err)
		}
	})

	t.Run("a Docker that does not start with IPv6 on", func(t *testing.T) {
		h := newRunnerHost(t, true, map[string]string{"systemctl": fakeLogger +
			`[ -f "$DAEMON_JSON" ] && exit 1
exit 0
`})
		out, ok := h.run(t, "export DAEMON_JSON="+h.daemonJSON+"\nsetup_host")
		if ok {
			t.Errorf("setup succeeded with a Docker that did not start:\n%s", out)
		}
		if _, err := os.Stat(h.daemonJSON); !os.IsNotExist(err) {
			t.Errorf("setup left the daemon.json Docker does not start with")
		}
		if calls := h.calls(t); strings.Count(calls, "systemctl restart docker") != 2 {
			t.Errorf("setup did not start Docker again without the file:\n%s", calls)
		}
	})
}
