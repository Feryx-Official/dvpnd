// SPDX-License-Identifier: Apache-2.0

package static

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// makeDryRun prints the commands make would run for target in dir, with env
// in place of the test's environment.
func makeDryRun(t *testing.T, dir string, env []string, target string) string {
	t.Helper()
	cmd := exec.Command("make", "--no-print-directory", "-n", target)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n %s: %v\n%s", target, err, out)
	}

	return string(out)
}

// TestMakefileTargets holds the Makefile's targets to doing what they are
// for. The version stamp cut a pre-release suffix; install wrote to /bin
// when GOPATH was unset, as it is unless set by hand; benchmark handed
// -mod=readonly to -bench as its pattern and ran nothing; go-lint rewrote
// files; and tools installed golangci-lint 1.50.1, which cannot read the
// version 2 configuration CI lints with. The Makefile runs here in a
// repository of the test's own, as a dry run.
func TestMakefileTargets(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "src")
	installerRepo(t, home, repo)
	if err := os.WriteFile(filepath.Join(repo, "Makefile"), read(t, "Makefile"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := installerGitEnv(home)

	t.Run("[CT-16] a build reports its tag's version, a pre-release suffix included", func(t *testing.T) {
		version := regexp.MustCompile(`version\.Version=(\S*)`)
		stamped := func() string {
			m := version.FindStringSubmatch(makeDryRun(t, repo, env, "build"))
			if m == nil {
				t.Fatal("make build stamps no version.Version")
			}

			return m[1]
		}
		commit := func() {
			installerGit(t, home, "-C", repo, "commit", "-q", "--allow-empty", "-m", "a change")
		}
		for _, c := range []struct {
			what string
			step func()
			want string
		}{
			{"on a release tag", func() { installerGit(t, home, "-C", repo, "tag", "v9.4.4") }, "9.4.4"},
			{"two commits past it", func() { commit(); commit() }, "9.4.4-2"},
			{"on a pre-release tag", func() { installerGit(t, home, "-C", repo, "tag", "v9.5.0-rc1") }, "9.5.0-rc1"},
			{"a commit past the pre-release", commit, "9.5.0-rc1-1"},
			{"on a tag that ends like a hash", func() { installerGit(t, home, "-C", repo, "tag", "v9.5.0-gabc") }, "9.5.0-gabc"},
		} {
			c.step()
			if got := stamped(); got != c.want {
				t.Errorf("%s: make build stamps %q, want %q", c.what, got, c.want)
			}
		}
	})

	t.Run("make install writes into Go's bin directory", func(t *testing.T) {
		var unset []string
		for _, kv := range env {
			if !strings.HasPrefix(kv, "GOPATH=") && !strings.HasPrefix(kv, "GOBIN=") {
				unset = append(unset, kv)
			}
		}
		out := makeDryRun(t, repo, unset, "install")
		if strings.Contains(out, " -o ") || !strings.Contains(out, "go install ") {
			t.Errorf("make install with GOPATH unset does not leave the place to go install:\n%s", out)
		}
	})

	t.Run("make benchmark runs the benchmarks", func(t *testing.T) {
		args := strings.Fields(makeDryRun(t, repo, env, "benchmark"))
		for i, arg := range args {
			if arg == "-bench" && (i+1 == len(args) || strings.HasPrefix(args[i+1], "-")) {
				t.Errorf("make benchmark gives -bench no pattern: %s", strings.Join(args, " "))
			}
		}
		if !strings.Contains(strings.Join(args, " "), "-bench") {
			t.Errorf("make benchmark runs no benchmark: %s", strings.Join(args, " "))
		}
	})

	t.Run("[RT-32] make tools installs the golangci-lint CI runs, and go-lint changes no file", func(t *testing.T) {
		lint := readWorkflow(t, ".github/workflows/ci.yml").job(t, "golangci/golangci-lint-action@", "runs golangci-lint")
		m := regexp.MustCompile(`(?m)^\s+version:\s*(v\S+)$`).FindStringSubmatch(lint.text)
		if m == nil {
			t.Fatal("CI's golangci-lint job pins no version")
		}
		want := "go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@" + m[1]
		if out := makeDryRun(t, repo, env, "tools"); !strings.Contains(out, want) {
			t.Errorf("make tools runs\n%s\nwant %s, the version CI lints with", out, want)
		}
		if out := makeDryRun(t, repo, env, "go-lint"); !strings.Contains(out, "golangci-lint run") || strings.Contains(out, "fix") {
			t.Errorf("make go-lint runs %q, want golangci-lint run without fixing files", strings.TrimSpace(out))
		}
	})
}
