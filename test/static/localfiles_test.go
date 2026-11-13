// SPDX-License-Identifier: Apache-2.0

package static

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// localPaths stand for what a contributor's checkout holds and the repository
// must not: notes at the root, a tool's settings and state in a dot-directory,
// and what a node or a test run leaves behind. The names are made up; the
// ignore files must catch them by pattern, never by naming a real file.
var localPaths = []string{
	"NOTES.md",
	"brief.md",
	".tool/settings.json",
	".tool/hooks/check.py",
	".env",
	"node.db",
	"data/node.db",
	"keyring-test/operator.info",
	"keyring-file/keyhash",
	"operator.key",
	"tls/node.key",
	"cover.out",
	"invariants/__pycache__/registry.cpython-312.pyc",
}

// sharedPaths are new files in places where shared ones belong; neither file
// may hide them.
var sharedPaths = []string{
	"README.md",
	"CONTRIBUTING.md",
	"SECURITY.md",
	"CODE_OF_CONDUCT.md",
	".github/workflows/new.yml",
	"docs/new.md",
	"test/README.md",
	"services/new/new.go",
}

// gitIgnored returns which of paths .gitignore alone ignores: it runs in a
// repository of its own, so neither this clone's .git/info/exclude nor a
// global excludes file can hide a gap.
func gitIgnored(t *testing.T, paths []string) map[string]bool {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), read(t, ".gitignore"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "HOME="+dir, "XDG_CONFIG_HOME="+dir, "GIT_CONFIG_NOSYSTEM=1")
	run := func(args ...string) []byte {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.Output()
		if err != nil && (len(args) == 0 || args[0] != "check-ignore") {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
		return out
	}
	run("init", "-q")
	ignored := map[string]bool{}
	for _, p := range strings.Split(string(run(append([]string{"check-ignore", "--no-index", "--"}, paths...)...)), "\n") {
		if p != "" {
			ignored[p] = true
		}
	}

	return ignored
}

// dockerIgnored reports whether Docker leaves path out of the build context:
// the last pattern that matches the path or one of its parent directories
// decides, a "!" pattern brings back what an earlier one left out, and "**/"
// matches any number of directories.
func dockerIgnored(patterns []string, path string) bool {
	ignored := false
	for _, pattern := range patterns {
		keep := strings.HasPrefix(pattern, "!")
		pattern = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(pattern, "!"), "/"), "/")
		if dockerMatch(pattern, path) {
			ignored = !keep
		}
	}

	return ignored
}

func dockerMatch(pattern, path string) bool {
	for p := path; p != "." && p != "/"; p = filepath.Dir(p) {
		if ok, _ := filepath.Match(pattern, p); ok {
			return true
		}
		if rest, ok := strings.CutPrefix(pattern, "**/"); ok {
			parts := strings.Split(p, "/")
			for i := range parts {
				if ok, _ := filepath.Match(rest, strings.Join(parts[i:], "/")); ok {
					return true
				}
			}
		}
	}

	return false
}

func dockerPatterns(t *testing.T) []string {
	t.Helper()
	var patterns []string
	for _, line := range strings.Split(string(read(t, ".dockerignore")), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			patterns = append(patterns, line)
		}
	}

	return patterns
}

// Rules: [LIC-9].
func TestLocalFilesStayOutOfCommits(t *testing.T) {
	ignored := gitIgnored(t, append(append([]string{}, localPaths...), sharedPaths...))
	for _, p := range localPaths {
		if !ignored[p] {
			t.Errorf(".gitignore lets %s be committed; local files stay out by pattern", p)
		}
	}
	for _, p := range sharedPaths {
		if ignored[p] {
			t.Errorf(".gitignore hides %s, where a shared file belongs", p)
		}
	}
	if out := git(t, "ls-files", "-ci", "--exclude-standard"); len(out) > 0 {
		t.Errorf("tracked files match an ignore pattern:\n%s", out)
	}
}

// Rules: [LIC-9].
func TestLocalFilesStayOutOfTheImageBuild(t *testing.T) {
	patterns := dockerPatterns(t)
	for _, p := range append([]string{".tool-versions", ".idea/workspace.xml"}, localPaths...) {
		if !dockerIgnored(patterns, p) {
			t.Errorf(".dockerignore sends %s into the image's build context", p)
		}
	}
	// What the build reads: the sources, the Makefile, and the repository
	// for the version stamp.
	needed := append([]string{"go.mod", "go.sum", "Makefile", ".git/HEAD", ".git/refs/tags/v9.0.0", ".git/objects/pack/x.pack"}, files(t, "*.go")...)
	for _, p := range needed {
		if dockerIgnored(patterns, p) {
			t.Errorf(".dockerignore leaves %s out, which the image build reads", p)
		}
	}
}

// TestDockerIgnored holds the model above to how Docker reads the patterns.
func TestDockerIgnored(t *testing.T) {
	patterns := []string{"/.*", "!/.git", "/*.md", "**/*.key", "/bin/"}
	for path, want := range map[string]bool{
		".env":            true,
		".tool/x":         true,
		".git/HEAD":       false,
		".github/x.yml":   true,
		"NOTES.md":        true,
		"docs/x.md":       false,
		"a.key":           true,
		"deep/dir/a.key":  true,
		"bin/dvpnd":       true,
		"cmd/bin/x":       false,
		"main.go":         false,
		"x.md.go":         false,
		"keyring/x.keys":  false,
		".gitignore":      true,
		"scripts/.hidden": false,
	} {
		if got := dockerIgnored(patterns, path); got != want {
			t.Errorf("%s: ignored = %v, want %v", path, got, want)
		}
	}
}

// privateNames are the SHA-256 sums of the lower-case words that name a
// contributor's private tooling or the local files that go with it: sums, so
// that this list names none of them itself.
var privateNames = map[string]bool{
	"c857d09db23e6822e3600bc06ad8d58f92ed62bc8efd81c753f77048662cb97d": true,
	"c70eca6b0f88f44d81a41311647e50fda1ac454ec04ffd442b0eb4743a993131": true,
	"57de4cf40144bdf7d00010f2f5557a7d642c2b9705309bfade167dd313e2ca93": true,
	"7d3194f79e645c42e4396dda38be04766810ec6a00d00aced3ffc2a0a1f1a9ef": true,
	"60965168ce762e949600281ba6d01fee136e5b6e8257b1f216f9025ed324474c": true,
	"3ea125d0bff386e6754b3782b300016fc79a9cf8f8669c0a5c3db64467ddb681": true,
}

var word = regexp.MustCompile(`[a-z0-9]+`)

// namingLines returns the numbers of the lines of text that hold a word whose
// sum is in names.
func namingLines(text string, names map[string]bool) []int {
	var lines []int
	for i, line := range strings.Split(strings.ToLower(text), "\n") {
		for _, w := range word.FindAllString(line, -1) {
			if sum := sha256.Sum256([]byte(w)); names[hex.EncodeToString(sum[:])] {
				lines = append(lines, i+1)
				break
			}
		}
	}

	return lines
}

// Rules: [LIC-10].
func TestNoPrivateToolingNamed(t *testing.T) {
	// Positive control: a word put on a list of its own is found, in any case
	// and inside a path, and a longer word that holds it is not.
	sum := sha256.Sum256([]byte("dvpnd"))
	control := map[string]bool{hex.EncodeToString(sum[:]): true}
	if got := namingLines("one\nsee ~/.DVPND/config.toml\nthe dvpndx tool\n", control); len(got) != 1 || got[0] != 2 {
		t.Fatalf("the scanner found lines %v; want [2]", got)
	}
	for s := range privateNames {
		if len(s) != 64 {
			t.Fatalf("%q is not a SHA-256 sum in hex", s)
		}
	}

	for _, f := range files(t, ".") {
		b := read(t, f)
		if bytes.IndexByte(b, 0) >= 0 { // binary
			continue
		}
		for _, n := range namingLines(string(b), privateNames) {
			t.Errorf("%s:%d names a contributor's private tooling or its local files; name what the repository needs as a dependency a reader installs", f, n)
		}
	}
}
