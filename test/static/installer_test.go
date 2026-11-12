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
