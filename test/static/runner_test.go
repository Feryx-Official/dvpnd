// SPDX-License-Identifier: Apache-2.0

package static

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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

// dockerFlags picks out of a docker run command line the flags the runner
// and the guide must agree on: capabilities, devices, sysctls, the modules
// volume, the restart policy and the log caps.
func dockerFlags(args []string) []string {
	var flags []string
	for i := 0; i+1 < len(args); i++ {
		switch flag, value := args[i], args[i+1]; flag {
		case "--cap-drop", "--cap-add", "--sysctl", "--device", "--restart", "--log-opt":
			flags = append(flags, flag+" "+value)
			i++
		case "--volume":
			if strings.HasPrefix(value, "/lib/modules") {
				flags = append(flags, flag+" "+value)
			}
			i++
		}
	}
	slices.Sort(flags)

	return flags
}

// guideFlags returns, by node type, the flags of the docker run command the
// operator guide's section 7 gives it.
func guideFlags(t *testing.T) map[string][]string {
	t.Helper()
	md := string(read(t, "docs/operator.md"))
	start, end := strings.Index(md, "\n## 7. "), strings.Index(md, "\n## 8. ")
	if start < 0 || end < start {
		t.Fatal("docs/operator.md has no section 7 followed by section 8")
	}
	section := md[start:end]
	paragraph := func(label string) string {
		i := strings.Index(section, label)
		if i < 0 {
			t.Fatalf("the guide's section 7 has no %s", label)
		}

		return section[i:]
	}
	command := func(label string) []string {
		rest := paragraph(label)
		open := strings.Index(rest, "```sh\n")
		if open < 0 {
			t.Fatalf("no command follows %s in the guide", label)
		}
		rest = rest[open+len("```sh\n"):]
		rest = rest[:strings.Index(rest, "```")]

		return dockerFlags(strings.Fields(strings.ReplaceAll(rest, "\\\n", " ")))
	}

	proxy := command("**V2Ray node:**")
	flags := map[string][]string{
		"wireguard": command("**WireGuard node:**"),
		"amneziawg": command("**AmneziaWG node:**"),
		// "XRAY node: the same command", "Hysteria2 node: the same command".
		"v2ray": proxy, "xray": proxy, "hysteria2": proxy,
	}
	// OpenVPN is written as the AmneziaWG command plus the capabilities its
	// paragraph names.
	openvpn := paragraph("**OpenVPN node:**")
	openvpn = openvpn[:strings.Index(openvpn, "\n\n")]
	flags["openvpn"] = slices.Clone(flags["amneziawg"])
	for _, m := range regexp.MustCompile(`--cap-add\s+([A-Z_]+)`).FindAllStringSubmatch(openvpn, -1) {
		flags["openvpn"] = append(flags["openvpn"], "--cap-add "+m[1])
	}
	slices.Sort(flags["openvpn"])

	return flags
}

// dockerRun returns the arguments of the one docker run in the fakes' log.
func dockerRun(t *testing.T, calls string) []string {
	t.Helper()
	var runs [][]string
	for _, call := range strings.Split(calls, "\n") {
		if f := strings.Fields(call); len(f) > 1 && f[0] == "docker" && f[1] == "run" {
			runs = append(runs, f[2:])
		}
	}
	if len(runs) != 1 {
		t.Fatalf("docker run ran %d times, want once:\n%s", len(runs), calls)
	}

	return runs[0]
}

// TestRunnerStartsTheGuidesContainer holds the Docker runner to the docker
// run command the operator guide gives each node type (section 7). It had
// drifted: no restart policy, so the node stayed down after a crash or a
// reboot; no log caps, so the log grew without bound; /lib/modules mounted
// writable. It found the node type anywhere after [node], and a moniker
// holding the word "type" (it precedes type in the file) was taken for it:
// nothing started, and start exited 0. cmd_start runs here against a fake
// docker, with a moniker holding the word.
//
// Rules: [RT-28].
func TestRunnerStartsTheGuidesContainer(t *testing.T) {
	guide := guideFlags(t)
	proxyFile := func(table string, port int) string {
		return fmt.Sprintf("[%s]\nlisten_port = %d\n\n[api]\nlisten_port = 10085\n", table, port)
	}
	for _, c := range []struct {
		name, nodeType, file, content string
		publish                       []string
	}{
		{"wireguard", "wireguard", "wireguard.toml", "listen_port = 51820\n", []string{"51820:51820/udp"}},
		{"amneziawg", "amneziawg", "amneziawg.toml",
			"listen_port = 51821\n\n[obfuscation]\njc = 4\n\n[v3]\nenabled = true\nlisten_port = 51822\n",
			[]string{"51821:51821/udp", "51822:51822/udp"}},
		{"amneziawg without the 3.1 tier", "amneziawg", "amneziawg.toml",
			"listen_port = 51821\n\n[v3]\nenabled = false\nlisten_port = 51822\n", []string{"51821:51821/udp"}},
		{"openvpn", "openvpn", "openvpn.toml", "listen_port = 1194\nproto = \"tcp\" # or udp\n", []string{"1194:1194/tcp"}},
		{"v2ray", "v2ray", "v2ray.toml", proxyFile("vmess", 8443), []string{"8443:8443/tcp"}},
		{"xray", "xray", "xray.toml", proxyFile("vless", 443), []string{"443:443/tcp"}},
		{"hysteria2", "hysteria2", "hysteria.toml", proxyFile("server", 4433), []string{"4433:4433/udp"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newRunnerHost(t, true, nil)
			files := map[string]string{
				"config.toml": "[keyring]\nbackend = \"test\"\n\n[node]\nlisten_on = \"0.0.0.0:8585\"\n" +
					"moniker = \"prototype node\"\ntype = \"" + c.nodeType + "\"\n",
				c.file: c.content,
			}
			for name, content := range files {
				if err := os.MkdirAll(filepath.Join(h.home, ".dvpnd"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(h.home, ".dvpnd", name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			out, ok := h.run(t, "cmd_start --detach")
			if !ok {
				t.Fatalf("start failed:\n%s", out)
			}
			args := dockerRun(t, h.calls(t))
			if got, want := dockerFlags(args), guide[c.nodeType]; !slices.Equal(got, want) {
				t.Errorf("the runner starts a %s node with\n  %v\nthe operator guide with\n  %v", c.nodeType, got, want)
			}
			var publish []string
			for i, a := range args {
				if a == "--publish" && i+1 < len(args) {
					publish = append(publish, args[i+1])
				}
			}
			if want := append([]string{"8585:8585/tcp"}, c.publish...); !slices.Equal(publish, want) {
				t.Errorf("the runner publishes %v, want %v", publish, want)
			}
			if !slices.Contains(args, "--detach") || !slices.Contains(args, "never") {
				t.Errorf("the runner's detached start is not detached, or may fetch an image unchecked: %v", args)
			}
		})
	}

	t.Run("in the foreground", func(t *testing.T) {
		h := newRunnerHost(t, true, nil)
		if err := os.MkdirAll(filepath.Join(h.home, ".dvpnd"), 0o700); err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string]string{
			"config.toml":    "[node]\nlisten_on = \"0.0.0.0:8585\"\ntype = \"wireguard\"\n",
			"wireguard.toml": "listen_port = 51820\n",
		} {
			if err := os.WriteFile(filepath.Join(h.home, ".dvpnd", name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if out, ok := h.run(t, "cmd_start"); !ok {
			t.Fatalf("start failed:\n%s", out)
		}
		args := dockerRun(t, h.calls(t))
		if !slices.Contains(args, "--rm") || slices.Contains(args, "--restart") || slices.Contains(args, "--detach") {
			t.Errorf("a foreground start must remove the container when it stops, with no restart policy: %v", args)
		}
	})
}

// fakeDocker holds the containers listed in $CONTAINERS, one "name status"
// per line, and matches docker ps --filter name= against part of a name, as
// Docker does; container inspect takes a whole name. It logs every call.
const fakeDocker = `#!/bin/sh
echo "docker $*" >>"$LOG"
case "$1" in
  ps)
    filter= all=0
    for a in "$@"; do
      case "$a" in name=*) filter=${a#name=} ;; --all | -a) all=1 ;; esac
    done
    while read -r name status; do
      [ -n "$name" ] || continue
      [ "$all" = 1 ] || [ "$status" = running ] || continue
      echo "$name" | grep -Eq -- "$filter" && echo "$name"
    done <"$CONTAINERS"
    ;;
  container | inspect)
    for name; do :; done
    while read -r n status; do
      [ "$n" = "$name" ] && { echo "$status"; exit 0; }
    done <"$CONTAINERS"
    echo "Error: No such container: $name" >&2
    exit 1
    ;;
esac
`

// TestRunnerActsOnItsOwnContainerOnly holds the Docker runner to the
// container named dvpnd. It found its container with docker ps --filter
// name=dvpnd, which matches part of a name, so stop, remove and restart acted
// on any container whose name held "dvpnd". The runner's commands run here
// against a fake docker holding another such container.
//
// Rules: [RT-28].
func TestRunnerActsOnItsOwnContainerOnly(t *testing.T) {
	for _, c := range []struct {
		containers string
		want       map[string]bool // command: whether it acts on dvpnd
	}{
		{"dvpnd-old running\nmy-dvpnd-backup exited\n", map[string]bool{"cmd_stop": false, "cmd_remove": false, "cmd_attach": false}},
		{"dvpnd-old running\ndvpnd running\n", map[string]bool{"cmd_stop": true, "cmd_remove": true, "cmd_attach": true}},
	} {
		for command, acts := range c.want {
			h := newRunnerHost(t, true, map[string]string{"docker": fakeDocker})
			list := filepath.Join(h.dir, "containers")
			if err := os.WriteFile(list, []byte(c.containers), 0o644); err != nil {
				t.Fatal(err)
			}
			out, ok := h.run(t, "export CONTAINERS="+list+"\n"+command)
			if ok != acts {
				t.Errorf("%s with %q succeeded = %v, want %v:\n%s", command, c.containers, ok, acts, out)
			}
			for _, call := range strings.Split(h.calls(t), "\n") {
				f := strings.Fields(call)
				if len(f) < 3 || !slices.Contains([]string{"stop", "rm", "attach"}, f[1]) {
					continue
				}
				if target := f[len(f)-1]; target != "dvpnd" {
					t.Errorf("%s acted on %s, another container: %s", command, target, call)
				}
			}
		}
	}
}

// fakeCosign passes or fails the signature check as $COSIGN says.
const fakeCosign = `#!/bin/sh
echo "cosign $*" >>"$LOG"
[ "$COSIGN" = pass ]
`

// TestRunnerChecksTheImageSignature holds the Docker runner to the image the
// repository's release workflow signed, as the operator guide checks it
// (section 7): it pulled :latest and ran whatever it got. With cosign
// installed, an image that fails the check is removed and the running node is
// left alone; without it, the runner says the image is not checked.
//
// Rules: [RT-28].
func TestRunnerChecksTheImageSignature(t *testing.T) {
	const digest = "ghcr.io/feryx-official/dvpnd@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	docker := fakeDocker + `[ "$1 $2" = "image inspect" ] && echo "` + digest + `"
exit 0
`
	for _, c := range []struct {
		what, cosign string
		ok           bool
	}{
		{"a signed image", "pass", true},
		{"an image the release workflow did not sign", "fail", false},
		{"no cosign installed", "", true},
	} {
		fakes := map[string]string{"docker": docker}
		if c.cosign != "" {
			fakes["cosign"] = fakeCosign
		}
		h := newRunnerHost(t, true, fakes)
		list := filepath.Join(h.dir, "containers")
		if err := os.WriteFile(list, []byte("dvpnd running\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, ok := h.run(t, "export CONTAINERS="+list+" COSIGN="+c.cosign+"\ncmd_update")
		calls := h.calls(t)
		if ok != c.ok {
			t.Errorf("%s: update succeeded = %v, want %v:\n%s\n%s", c.what, ok, c.ok, out, calls)
		}
		if c.cosign != "" && !strings.Contains(calls, "cosign verify "+digest+" ") {
			t.Errorf("%s: the image's digest was not checked:\n%s", c.what, calls)
		}
		stopped := strings.Contains(calls, "docker stop dvpnd")
		if stopped != c.ok {
			t.Errorf("%s: the running node was stopped = %v, want %v:\n%s", c.what, stopped, c.ok, calls)
		}
		if removed := strings.Contains(calls, "docker image rm"); removed == c.ok {
			t.Errorf("%s: the image was removed = %v, want %v:\n%s", c.what, removed, !c.ok, calls)
		}
		if c.cosign == "" && !strings.Contains(out, "not checked") {
			t.Errorf("without cosign the runner did not say the image is not checked:\n%s", out)
		}
	}
}
