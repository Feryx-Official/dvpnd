// SPDX-License-Identifier: Apache-2.0

package static

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// canaryRun runs invariants/canaries.py over a tree of the test's own, a git
// repository with no commit holding files, with config as its canaries.json.
// It returns what the runner printed, its exit status and how long it took.
func canaryRun(t *testing.T, files map[string]string, config string) (string, int, time.Duration) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatalf("python3 runs the canaries in make verify and is not on PATH: %v", err)
	}
	tree := t.TempDir()
	if out, err := exec.Command("git", "-c", "init.defaultBranch=main", "-C", tree, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(tree, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := filepath.Join(t.TempDir(), "canaries.json")
	if err := os.WriteFile(cfg, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	out, err := exec.Command(python, filepath.Join(root(t), "invariants", "canaries.py"), "--config", cfg, "--root", tree).CombinedOutput()
	took := time.Since(started)
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		return string(out), exit.ExitCode(), took
	case err != nil:
		t.Fatal(err)
	}

	return string(out), 0, took
}

// verdict returns the verdict the runner printed for the canary named name.
func verdict(out, name string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutSuffix(line, " "+name); ok {
			return strings.TrimSpace(v)
		}
	}

	return ""
}

// TestCanaryCountsOnlyAFailureTheMutationCaused holds the canary runner to
// running each canary's commands once on a copy without the mutation, where
// they must pass: a command that fails anyway would count as catching any
// mutation. The two LIC-8 canaries were counted as caught that way, since their
// test lists the files with git and the copy was not a git repository; so the
// copy is one, with no commit.
func TestCanaryCountsOnlyAFailureTheMutationCaused(t *testing.T) {
	const config = `{"timeout": 120, "canaries": [
	  {"name": "sound", "file": "rule.txt", "find": "guarded", "replace": "removed",
	   "run": ["grep -q guarded rule.txt"]},
	  {"name": "lists the tree with git", "file": "rule.txt", "find": "guarded", "replace": "removed",
	   "run": ["git ls-files -co --exclude-standard | grep -qx rule.txt", "grep -q guarded rule.txt"]},
	  {"name": "fails anyway", "file": "rule.txt", "find": "guarded", "replace": "removed",
	   "run": ["grep -q unrelated rule.txt"]}
	]}`
	out, code, _ := canaryRun(t, map[string]string{"rule.txt": "guarded\n"}, config)
	for name, want := range map[string]string{
		"sound":                   "caught",
		"lists the tree with git": "caught",
		"fails anyway":            "RED BEFORE",
	} {
		if got := verdict(out, name); got != want {
			t.Errorf("canary %q: verdict %q, want %q", name, got, want)
		}
	}
	if code != 1 {
		t.Errorf("the runner exited %d with a canary that proves nothing, want 1", code)
	}
	if t.Failed() {
		t.Logf("runner output:\n%s", out)
	}
}
