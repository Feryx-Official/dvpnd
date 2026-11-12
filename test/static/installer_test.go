// SPDX-License-Identifier: Apache-2.0

package static

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// aptGetRun matches apt-get run as a command, not named in a check for it.
var aptGetRun = regexp.MustCompile(`(^|[^-\w])apt-get\s`)

// fakeAptGet logs its arguments and answers as apt does while another run
// holds its locks: update finds the package lists' lock taken twice, install
// gets the dpkg lock only when told to wait for it, and a package that does
// not exist fails the way it always will.
const fakeAptGet = `#!/bin/sh
echo "$*" >>"$LOG"
case " $* " in
  *" nosuchpackage "*) echo "E: Unable to locate package nosuchpackage" >&2; exit 100 ;;
  *" update "*) [ "$(wc -l <"$LOG")" -gt 2 ] || { echo "E: Could not get lock /var/lib/apt/lists/lock" >&2; exit 100; } ;;
  *" install "*) case "$*" in *"-o DPkg::Lock::Timeout="*) ;; *) echo "E: Could not get lock /var/lib/dpkg/lock-frontend" >&2; exit 100 ;; esac ;;
esac
`

// TestInstallerWaitsForThePackageLock holds the installer to waiting while
// another apt run holds the package manager, as unattended-upgrades does in a
// fresh server's first minutes: every apt-get goes through apt_get, which
// waits for the dpkg lock and retries update, since apt itself waits for the
// dpkg lock only. The wrapper runs here against a fake apt-get.
//
// Rules: [RT-11].
func TestInstallerWaitsForThePackageLock(t *testing.T) {
	const file = "scripts/install.sh"
	lines := strings.Split(string(read(t, file)), "\n")
	start, end := -1, -1
	for i, line := range lines {
		if line == "apt_get() {" {
			start = i
		} else if start >= 0 && line == "}" {
			end = i
			break
		}
	}

	runs := 0
	for i, line := range lines {
		code, _, _ := strings.Cut(line, "#")
		if !aptGetRun.MatchString(code) || strings.Contains(code, "command -v apt-get") {
			continue
		}
		runs++
		if start < 0 || i < start || i > end {
			t.Errorf("%s:%d runs apt-get itself; use apt_get, which waits while another apt run holds the lock: %s",
				file, i+1, strings.TrimSpace(line))
		}
	}
	if runs == 0 {
		t.Fatalf("%s runs no apt-get; this test reads the wrong file", file)
	}
	if start < 0 || end < 0 {
		t.Fatalf("%s defines no apt_get() { ... } function to run apt-get through", file)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "apt-get"), []byte(fakeAptGet), 0o755); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(dir, "log")
	wrapper := strings.Join(lines[start:end+1], "\n")
	run := func(args string) error {
		script := "set -Eeuo pipefail\nsleep() { :; }\n" + wrapper + "\napt_get " + args + "\n"
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "LOG="+logFile)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("apt_get %s: %v\n%s", args, err, out)
		}

		return err
	}

	if err := run("update -qq"); err != nil {
		t.Error("apt_get update gave up while the package lists' lock was taken")
	}
	if err := run("install -y -qq ufw"); err != nil {
		t.Error("apt_get install did not wait for the dpkg lock")
	}
	if err := run("install -y -qq nosuchpackage"); err == nil {
		t.Error("apt_get install reported a package that does not exist as installed")
	}
	logged, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(logged)), "\n")
	if len(calls) != 5 {
		t.Errorf("apt-get ran %d times, want 5 (update three times, then each install once):\n%s", len(calls), strings.Join(calls, "\n"))
	}
}

// shellFunction returns the lines of the function name defines in a shell
// script, from "name() {" to its closing "}", or nothing if it has none.
func shellFunction(lines []string, name string) (start, end int) {
	start, end = -1, -1
	for i, line := range lines {
		if line == name+"() {" {
			start = i
		} else if start >= 0 && line == "}" {
			return start, i
		}
	}

	return -1, -1
}

// TestInstallerTrustsTheSourceOnce holds the installer to one safe.directory
// entry for the tree it builds, however often it runs: an upgrade is a re-run,
// and each run used to add another copy to root's git configuration. A run
// also folds the copies earlier runs left into one, and leaves the operator's
// own entries alone. trust_source runs here against a git configuration of
// the test's own.
//
// Rules: [RT-12].
func TestInstallerTrustsTheSourceOnce(t *testing.T) {
	const file = "scripts/install.sh"
	lines := strings.Split(string(read(t, file)), "\n")
	start, end := shellFunction(lines, "trust_source")
	if start < 0 {
		t.Fatalf("%s defines no trust_source() { ... } function to add the safe.directory entry", file)
	}
	for i, line := range lines {
		if code, _, _ := strings.Cut(line, "#"); strings.Contains(code, "safe.directory") && (i < start || i > end) {
			t.Errorf("%s:%d changes safe.directory outside trust_source: %s", file, i+1, strings.TrimSpace(line))
		}
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src.tree")
	if err := os.MkdirAll(filepath.Join(src, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitConfig := filepath.Join(dir, "gitconfig")
	env := append(os.Environ(), "HOME="+dir, "XDG_CONFIG_HOME="+dir, "GIT_CONFIG_GLOBAL="+gitConfig,
		"GIT_CONFIG_NOSYSTEM=1", "SRC="+src)
	trustSource := strings.Join(lines[start:end+1], "\n")
	run := func() {
		cmd := exec.Command("bash", "-c", "set -Eeuo pipefail\n"+trustSource+"\ntrust_source\n")
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("trust_source: %v\n%s", err, out)
		}
	}
	entries := func() []string {
		cmd := exec.Command("git", "config", "--global", "--get-all", "safe.directory")
		cmd.Env = env
		out, _ := cmd.Output()

		return strings.Fields(string(out))
	}

	run()
	run()
	if got := entries(); len(got) != 1 || got[0] != src {
		t.Errorf("after two runs from no entry, safe.directory is %q, want only %q", got, src)
	}

	// The operator's entries, one of which only a pattern would take for src,
	// and two copies of src that earlier runs left.
	other, nearMiss := filepath.Join(dir, "other"), filepath.Join(dir, "srcXtree")
	seeded := "[safe]\n\tdirectory = " + other + "\n\tdirectory = " + src + "\n\tdirectory = " + nearMiss +
		"\n\tdirectory = " + src + "\n"
	if err := os.WriteFile(gitConfig, []byte(seeded), 0o600); err != nil {
		t.Fatal(err)
	}
	run()
	got, want := entries(), []string{other, src, nearMiss}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("after a run over two copies, safe.directory is %q, want %q in any order", got, want)
	}
}

// installerGitEnv is an environment in which git reads no configuration but
// the test's own, under home, and commits and tags as a fixed identity.
func installerGitEnv(home string) []string {
	return append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+home, "GIT_CONFIG_GLOBAL="+filepath.Join(home, "gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1", "SSH_AUTH_SOCK=", "GIT_AUTHOR_NAME=Maintainer", "GIT_AUTHOR_EMAIL=maintainer@example.org",
		"GIT_COMMITTER_NAME=Maintainer", "GIT_COMMITTER_EMAIL=maintainer@example.org")
}

// installerGit runs git in installerGitEnv and returns its trimmed output.
func installerGit(t *testing.T, home string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = installerGitEnv(home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}

	return strings.TrimSpace(string(out))
}

// installerRepo makes a repository at path holding one commit.
func installerRepo(t *testing.T, home, path string) {
	t.Helper()
	installerGit(t, home, "-c", "init.defaultBranch=main", "init", "-q", path)
	if err := os.WriteFile(filepath.Join(path, "go.mod"), []byte("module example.org/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installerGit(t, home, "-C", path, "add", "go.mod")
	installerGit(t, home, "-C", path, "commit", "-q", "-m", "a release")
}

// TestInstallerBuildsOnlyTheSignedTagAskedFor holds verify_tag to the tag the
// operator asked for: signed with the maintainer's key, and naming itself that
// tag. The signature covers the tag object, not the ref that points at it, so
// an older release's signed tag fetched under a newer name passed and was
// built as root. verify_tag runs here against tags signed with keys of the
// test's own.
//
// Rules: [RT-14].
func TestInstallerBuildsOnlyTheSignedTagAskedFor(t *testing.T) {
	const file = "scripts/install.sh"
	lines := strings.Split(string(read(t, file)), "\n")
	start, end := shellFunction(lines, "verify_tag")
	if start < 0 {
		t.Fatalf("%s defines no verify_tag() { ... } function to check the release tag", file)
	}

	home := t.TempDir()
	keys := map[string]string{}
	for _, name := range []string{"maintainer", "someone"} {
		keys[name] = filepath.Join(home, name)
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", keys[name]).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v\n%s", err, out)
		}
	}
	b, err := os.ReadFile(keys["maintainer"] + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	pub := strings.Fields(string(b))
	signer := "maintainer@example.org " + pub[0] + " " + pub[1]

	repo := filepath.Join(home, "src")
	installerRepo(t, home, repo)
	tag := func(name, message, key string) {
		args := []string{"-C", repo, "tag", "-a"}
		if key != "" {
			args = []string{"-C", repo, "-c", "gpg.format=ssh", "-c", "user.signingkey=" + key, "tag", "-s"}
		}
		installerGit(t, home, append(args, "-m", message, name)...)
	}
	tag("v1.0.0", "an old release", keys["maintainer"])
	tag("v3.0.0", "a release\n\ntag v4.0.0", keys["maintainer"])
	tag("v5.0.0", "not signed", "")
	tag("v6.0.0", "signed with another key", keys["someone"])
	installerGit(t, home, "-C", repo, "tag", "v7.0.0")
	for alias, of := range map[string]string{"v2.0.0": "v1.0.0", "v4.0.0": "v3.0.0"} {
		installerGit(t, home, "-C", repo, "update-ref", "refs/tags/"+alias, installerGit(t, home, "-C", repo, "rev-parse", "refs/tags/"+of))
	}

	verifyTag := strings.Join(lines[start:end+1], "\n")
	for _, c := range []struct {
		tag, what string
		accept    bool
	}{
		{"v1.0.0", "signed by the maintainer under its own name", true},
		{"v3.0.0", "signed by the maintainer under its own name, another tag named in its message", true},
		{"v2.0.0", "the maintainer's signed v1.0.0 under another name", false},
		{"v4.0.0", "the maintainer's signed v3.0.0 under the name its message holds", false},
		{"v5.0.0", "not signed", false},
		{"v6.0.0", "signed with another key", false},
		{"v7.0.0", "a lightweight tag, nothing signed", false},
		{"v8.0.0", "no such tag", false},
	} {
		cmd := exec.Command("bash", "-c", "set -Eeuo pipefail\n"+verifyTag+"\nverify_tag \"$1\" \"$2\" || exit 1\n", "verify_tag", repo, c.tag)
		cmd.Env = append(installerGitEnv(home), "TAG_SIGNER="+signer)
		out, err := cmd.CombinedOutput()
		if accepted := err == nil; accepted != c.accept {
			t.Errorf("verify_tag %s, %s: accepted = %v, want %v\n%s", c.tag, c.what, accepted, c.accept, out)
		}
	}
}

// TestInstallerFetchesFromTheCurrentHome holds fetch_source to the
// repository's current home: an earlier clone keeps the origin it was cloned
// from, and an upgrade fetched from there, a former home that serves whatever
// is put there next (LIC-8). fetch_source runs here against repositories of
// the test's own.
//
// Rules: [RT-14].
func TestInstallerFetchesFromTheCurrentHome(t *testing.T) {
	const file = "scripts/install.sh"
	lines := strings.Split(string(read(t, file)), "\n")
	start, end := shellFunction(lines, "fetch_source")
	if start < 0 {
		t.Fatalf("%s defines no fetch_source() { ... } function to fetch the source", file)
	}
	for i, line := range lines {
		code, _, _ := strings.Cut(line, "#")
		source := strings.Contains(code, "${REPO_URL}") || strings.Contains(code, " fetch ") && strings.Contains(code, "${SRC}")
		if source && (i < start || i > end) {
			t.Errorf("%s:%d fetches the source outside fetch_source: %s", file, i+1, strings.TrimSpace(line))
		}
	}

	home := t.TempDir()
	former, current := filepath.Join(home, "former"), filepath.Join(home, "current")
	installerRepo(t, home, former)
	installerRepo(t, home, current)
	installerGit(t, home, "-C", current, "tag", "v2.0.0")

	fetchSource := strings.Join(lines[start:end+1], "\n")
	for _, c := range []struct {
		what    string
		prepare func(src string)
	}{
		{"a first install", func(string) {}},
		{"an upgrade of a clone of a former home", func(src string) {
			installerGit(t, home, "clone", "-q", former, src)
		}},
		{"an upgrade of a clone with no origin", func(src string) {
			installerGit(t, home, "clone", "-q", former, src)
			installerGit(t, home, "-C", src, "remote", "remove", "origin")
		}},
	} {
		src := filepath.Join(t.TempDir(), "src")
		c.prepare(src)
		cmd := exec.Command("bash", "-c", "set -Eeuo pipefail\n"+fetchSource+"\nfetch_source\n")
		cmd.Env = append(installerGitEnv(home), "REPO_URL="+current, "SRC="+src)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: fetch_source: %v\n%s", c.what, err, out)
			continue
		}
		if got := installerGit(t, home, "-C", src, "remote", "get-url", "origin"); got != current {
			t.Errorf("%s: origin is %s after fetch_source, want the current home %s", c.what, got, current)
		}
		if out, err := exec.Command("git", "-C", src, "rev-parse", "-q", "--verify", "refs/tags/v2.0.0").CombinedOutput(); err != nil {
			t.Errorf("%s: the current home's tag v2.0.0 was not fetched: %v\n%s", c.what, err, out)
		}
	}
}

// installerFunction returns the source of the function name defines in the
// installer, failing the test when it has none.
func installerFunction(t *testing.T, name string) string {
	t.Helper()
	const file = "scripts/install.sh"
	lines := strings.Split(string(read(t, file)), "\n")
	start, end := shellFunction(lines, name)
	if start < 0 {
		t.Fatalf("%s defines no %s() { ... } function", file, name)
	}

	return strings.Join(lines[start:end+1], "\n")
}

// fakeSSHD answers sshd -T with the contents of $SSHD_T, and fails as an sshd
// that cannot read its configuration does when there is none.
const fakeSSHD = `#!/bin/sh
[ "$1" = -T ] && [ -f "$SSHD_T" ] && exec cat "$SSHD_T"
echo "sshd: no hostkeys available -- exiting." >&2
exit 1
`

// TestInstallerOpensTheSSHPortsSSHDListensOn holds the installer to the ports
// sshd listens on, as sshd reads its own configuration, before it turns ufw
// on. Read from the main file's first Port line, a port set in
// /etc/ssh/sshd_config.d or by ListenAddress was missed: ufw opened 22 only,
// and enabling it shut the operator out. ssh_ports runs here against a fake
// sshd and configuration files of the test's own.
//
// Rules: [RT-24].
func TestInstallerOpensTheSSHPortsSSHDListensOn(t *testing.T) {
	sshPorts := installerFunction(t, "ssh_ports")
	for _, c := range []struct {
		what         string
		sshdT        string // what sshd -T prints; empty for an sshd that fails
		main, dropIn string
		want         string
	}{
		{"a port set in a drop-in file", "port 2222\nlistenaddress [::]:2222\nlistenaddress 0.0.0.0:2222\n",
			"Include /etc/ssh/sshd_config.d/*.conf\n#Port 22\n", "Port 2222\n", "2222"},
		{"a ListenAddress with a port of its own", "port 2222\nlistenaddress 0.0.0.0:2200\n",
			"Port 2222\nListenAddress 0.0.0.0:2200\n", "", "2200"},
		{"two ports", "port 22\nport 2222\nlistenaddress [::]:22\nlistenaddress [::]:2222\nlistenaddress 0.0.0.0:22\nlistenaddress 0.0.0.0:2222\n",
			"Port 22\nPort 2222\n", "", "22 2222"},
		{"an sshd that cannot tell, the port in a drop-in file", "",
			"Include /etc/ssh/sshd_config.d/*.conf\n#Port 22\n", "Port 2022\n", "2022"},
		{"an sshd that cannot tell, no port anywhere", "", "", "", "22"},
	} {
		dir := t.TempDir()
		bin := filepath.Join(dir, "bin")
		if err := os.MkdirAll(filepath.Join(dir, "sshd_config.d"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		write := func(name, content string, mode os.FileMode) {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), mode); err != nil {
				t.Fatal(err)
			}
		}
		write("bin/sshd", fakeSSHD, 0o755)
		write("sshd_config", c.main, 0o644)
		write("sshd_config.d/50-port.conf", c.dropIn, 0o644)
		if c.sshdT != "" {
			write("sshd-T", c.sshdT, 0o644)
		}

		cmd := exec.Command("bash", "-c", "set -Eeuo pipefail\n"+sshPorts+"\nssh_ports\n")
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"SSHD_T="+filepath.Join(dir, "sshd-T"), "SSHD_CONFIG="+filepath.Join(dir, "sshd_config"))
		out, err := cmd.Output()
		if err != nil {
			t.Errorf("%s: ssh_ports: %v", c.what, err)
			continue
		}
		if got := strings.Join(strings.Fields(string(out)), " "); got != c.want {
			t.Errorf("%s: ssh_ports gives %q, want %q: ufw would shut out the operator", c.what, got, c.want)
		}
	}
}

// fakeDvpnd stands for the dvpnd binary's config commands: init writes the
// file, set adds a line, and a set to the value "fail" fails, as a key the
// binary does not have does (RT-13).
const fakeDvpnd = `#!/bin/sh
home=$2; shift 2
file=config.toml
[ "$1" = config ] || { file=$1.toml; shift; }
case "$2" in
  init) echo "# written by init" >"$home/$file" ;;
  set) [ "$4" = fail ] && { echo "Error: invalid key $3" >&2; exit 1; }; echo "$3 = $4" >>"$home/$file" ;;
esac
`

// TestInstallerWritesTheConfigurationWholeOrNotAtAll holds the installer to
// writing config.toml and the protocol file whole: a command that failed
// after config init left a partial file, which the next run kept as the
// node's configuration. write_config and write_protocol run here against a
// fake dvpnd.
//
// Rules: [RT-25].
func TestInstallerWritesTheConfigurationWholeOrNotAtAll(t *testing.T) {
	var functions string
	for _, name := range []string{"dvs", "new_stage", "proto_file", "write_config", "write_protocol"} {
		functions += installerFunction(t, name) + "\n"
	}
	bin := filepath.Join(t.TempDir(), "dvpnd")
	if err := os.WriteFile(bin, []byte(fakeDvpnd), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(home, call string, env ...string) error {
		cmd := exec.Command("bash", "-c", "set -Eeuo pipefail\n"+functions+"\n"+call+"\n")
		cmd.Env = append(os.Environ(), "DVPND="+bin, "NODE_HOME="+home, "KEY_NAME=operator", "MONIKER=My node",
			"NODE_TYPE=openvpn", "HOURLY_PRICES=97500000udvpn", "API_PORT=8585", "PUBLIC_IP=203.0.113.1",
			"LISTEN_PORT=51820", "V3_LISTEN_PORT=", "IPV6_OK=false", "FORCE=0", "GIGABYTE_PRICES=40000000udvpn",
			"OPENVPN_PROTO=udp")
		cmd.Env = append(cmd.Env, env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("%s: %v\n%s", call, err, out)
		}

		return err
	}
	content := func(path string) string {
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return ""
		} else if err != nil {
			t.Fatal(err)
		}

		return string(b)
	}

	for _, c := range []struct {
		call, file, failing, good, last string
	}{
		{"write_config", "config.toml", "GIGABYTE_PRICES", "40000000udvpn", "node.remote_url = https://203.0.113.1:8585"},
		{"write_protocol", "openvpn.toml", "OPENVPN_PROTO", "udp", "proto = udp"},
	} {
		home := t.TempDir()
		path := filepath.Join(home, c.file)
		if err := run(home, c.call, c.failing+"=fail"); err == nil {
			t.Errorf("%s reported success with a setting that failed", c.call)
		}
		if got := content(path); got != "" {
			t.Errorf("%s left a partial %s after a setting failed, which the next run keeps:\n%s", c.call, c.file, got)
		}
		if err := run(home, c.call); err != nil {
			t.Errorf("%s failed with every setting good", c.call)
		}
		written := content(path)
		if !strings.Contains(written, c.last) {
			t.Errorf("the next %s wrote %s without its last setting %q:\n%s", c.call, c.file, c.last, written)
		}
		if entries, _ := os.ReadDir(home); len(entries) != 1 {
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("%s left %v in the node's home, want only %s", c.call, names, c.file)
		}

		// A forced rewrite that fails keeps the file the node has.
		if err := run(home, c.call, "FORCE=1", c.failing+"=fail"); err == nil {
			t.Errorf("%s --force reported success with a setting that failed", c.call)
		}
		if got := content(path); got != written {
			t.Errorf("%s --force with a setting that failed replaced %s:\n%s", c.call, c.file, got)
		}
	}
}

// TestInstallerShowsTheMnemonicOnATerminalOnly holds the installer to showing
// the new operator key's mnemonic, the wallet's only backup, on a terminal
// only. Run under cloud-init, through tee or from a CI job, it printed the
// mnemonic into a log; there it goes to a file only root can read, named in
// the output. create_key runs here against a fake dvpnd, with its output
// going to a pipe.
//
// Rules: [PV-12].
func TestInstallerShowsTheMnemonicOnATerminalOnly(t *testing.T) {
	const mnemonic = "abandon ability able about above absent absorb abstract absurd abuse access accident"
	createKey := installerFunction(t, "dv") + "\n" + installerFunction(t, "create_key")
	home := t.TempDir()
	bin := filepath.Join(t.TempDir(), "dvpnd")
	fake := "#!/bin/sh\necho '- name: operator'\necho '**Important** write this mnemonic phrase in a safe place' >&2\necho '" +
		mnemonic + "' >&2\n"
	if err := os.WriteFile(bin, []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", "set -Eeuo pipefail\n"+`log() { printf '\n==> %s\n' "$*"; }`+"\n"+
		`die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }`+"\n"+createKey+"\ncreate_key\n")
	cmd.Env = append(os.Environ(), "DVPND="+bin, "NODE_HOME="+home, "KEY_NAME=operator", "YES=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create_key: %v\n%s", err, out)
	}
	if strings.Contains(string(out), mnemonic) {
		t.Errorf("create_key printed the mnemonic to output that is not a terminal, where a log keeps it:\n%s", out)
	}

	var saved string
	entries, _ := os.ReadDir(home)
	for _, e := range entries {
		path := filepath.Join(home, e.Name())
		b, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(b), mnemonic) {
			continue
		}
		saved = path
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s holds the mnemonic with mode %v, want 0600", path, info.Mode().Perm())
		}
	}
	if saved == "" {
		t.Fatalf("create_key kept the mnemonic nowhere: the wallet has no backup\n%s", out)
	}
	if !strings.Contains(string(out), saved) {
		t.Errorf("create_key did not say where the mnemonic is (%s):\n%s", saved, out)
	}
}

// assignment matches a shell variable set to a value, alone or in a case
// branch.
var assignment = regexp.MustCompile(`(?:^|[\s;)])([A-Za-z_][A-Za-z0-9_]*)=("[^"]*"|[^\s;]*)`)

// TestInstallerDownloadsOnlyWhatItPins holds the installer, which runs as
// root, to downloads it can trust: each one goes to a directory of its own
// (a fixed path under /tmp can be put there first by any local user), and
// each checksum is one the script pins (the Go checksum came from the same
// site as the tarball, so it proved the download intact, not authentic).
//
// Rules: [RT-26].
func TestInstallerDownloadsOnlyWhatItPins(t *testing.T) {
	const file = "scripts/install.sh"
	lines := strings.Split(string(read(t, file)), "\n")
	values := map[string][]string{}
	for _, line := range lines {
		code, _, _ := strings.Cut(line, "#")
		for _, m := range assignment.FindAllStringSubmatch(code, -1) {
			values[m[1]] = append(values[m[1]], strings.Trim(m[2], `"`))
		}
	}
	pinned := regexp.MustCompile(`^[0-9a-f]{64}$`)
	reference := regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)
	var isPinned func(name string, depth int) bool
	isPinned = func(name string, depth int) bool {
		if depth > 3 || len(values[name]) == 0 {
			return false
		}
		for _, v := range values[name] {
			if m := reference.FindStringSubmatch(v); m != nil {
				if !isPinned(m[1], depth+1) {
					return false
				}
			} else if !pinned.MatchString(v) {
				return false
			}
		}

		return true
	}

	checked := regexp.MustCompile(`echo "\$\{([A-Za-z_][A-Za-z0-9_]*)\}\s+[^"]*" \| sha256sum -c`)
	checks := 0
	for i, line := range lines {
		code, _, _ := strings.Cut(line, "#")
		if strings.Contains(code, "/tmp/") || strings.Contains(code, "/tmp ") {
			t.Errorf("%s:%d uses a fixed path under /tmp as root: %s", file, i+1, strings.TrimSpace(line))
		}
		if !strings.Contains(code, "sha256sum") {
			continue
		}
		checks++
		m := checked.FindStringSubmatch(code)
		if m == nil {
			t.Errorf("%s:%d checks a download against something other than a variable: %s", file, i+1, strings.TrimSpace(line))
		} else if !isPinned(m[1], 0) {
			t.Errorf("%s:%d checks a download against %s, which is not a sha256 pinned in the script: %s",
				file, i+1, m[1], strings.TrimSpace(line))
		}
	}
	if checks < 3 {
		t.Fatalf("%s checks %d downloads, want the Go tarball, xray and hysteria at least", file, checks)
	}
}
