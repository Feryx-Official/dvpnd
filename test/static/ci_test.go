// SPDX-License-Identifier: Apache-2.0

package static

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// workflowJob is one job of a GitHub workflow: its ID, the keys set on the
// job itself (name, runs-on, needs, timeout-minutes, ...) and its whole text.
type workflowJob struct {
	id   string
	keys map[string]string
	text string
}

// check is the name the job's check run carries: its name, or its ID.
func (j workflowJob) check() string {
	if name := j.keys["name"]; name != "" {
		return name
	}

	return j.id
}

// workflow is what the tests read from a GitHub workflow file: its top-level
// keys with their text, the events in its on: block and its jobs.
type workflow struct {
	file string
	top  map[string]string
	on   map[string]bool
	jobs []workflowJob
}

// job returns the workflow's job whose text holds marker, failing the test
// when none does.
func (w workflow) job(t *testing.T, marker, what string) workflowJob {
	t.Helper()
	for _, j := range w.jobs {
		if strings.Contains(j.text, marker) {
			return j
		}
	}
	t.Fatalf("%s has no job that %s (%s)", w.file, what, marker)

	return workflowJob{}
}

// yamlValue is a scalar as a workflow writes it, without its comment or quotes.
func yamlValue(s string) string {
	if i := strings.Index(s, " #"); i >= 0 {
		s = s[:i]
	}

	return strings.Trim(strings.TrimSpace(s), `"'`)
}

// readWorkflow reads a workflow in the block style the ones here use, with
// two-space indents. A line it cannot place fails the test.
func readWorkflow(t *testing.T, file string) workflow {
	t.Helper()
	w := workflow{file: file, top: map[string]string{}, on: map[string]bool{}}
	key := regexp.MustCompile(`^( *)([A-Za-z0-9_-]+):(?:\s+(.*))?$`)
	section, job := "", -1
	for i, line := range strings.Split(string(read(t, file)), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		m := key.FindStringSubmatch(line)
		if indent == 0 {
			if m == nil {
				t.Fatalf("%s:%d: not a top-level key: %s", file, i+1, line)
			}
			section, job = m[2], -1
			w.top[section] = yamlValue(m[3])
			continue
		}
		w.top[section] += "\n" + line
		switch section {
		case "on":
			if indent == 2 && m != nil {
				w.on[m[2]] = true
			}
		case "jobs":
			if indent == 2 && m != nil {
				w.jobs = append(w.jobs, workflowJob{id: m[2], keys: map[string]string{}})
				job = len(w.jobs) - 1
				continue
			}
			if job < 0 {
				t.Fatalf("%s:%d: outside any job: %s", file, i+1, line)
			}
			w.jobs[job].text += line + "\n"
			if indent == 4 && m != nil {
				w.jobs[job].keys[m[2]] = yamlValue(m[3])
			}
		}
	}
	if len(w.jobs) == 0 {
		t.Fatalf("%s: no jobs read", file)
	}

	return w
}

// fakeGovulncheck prints $REPORT and exits with $STATUS, as govulncheck
// prints what it found and exits 3 when the code reaches a vulnerability.
const fakeGovulncheck = "#!/bin/sh\nprintf '%s\\n' \"$REPORT\"\nexit \"$STATUS\"\n"

// govulnReport is govulncheck's text output for the advisories ids, each
// reachable from the code.
func govulnReport(ids ...string) string {
	var b strings.Builder
	b.WriteString("=== Symbol Results ===\n\n")
	for i, id := range ids {
		fmt.Fprintf(&b, "Vulnerability #%d: %s\n    A description.\n  More info: https://pkg.go.dev/vuln/%s\n\n", i+1, id, id)
	}
	fmt.Fprintf(&b, "Your code is affected by %d vulnerabilities.\n", len(ids))

	return b.String()
}

// TestGovulncheckFailsOnUnexplainedAdvisories holds CI's govulncheck step
// to the advisories SECURITY.md explains: the job ran govulncheck under
// set +e and only warned, so a pull request that reached a new vulnerability
// passed. The script runs here from a root of the test's own, whose
// SECURITY.md explains one advisory, against a fake govulncheck.
//
// Rules: [RT-32].
func TestGovulncheckFailsOnUnexplainedAdvisories(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "scripts", "govulncheck.sh")
	fake := filepath.Join(dir, "govulncheck")
	for path, content := range map[string]string{
		script: string(read(t, "scripts/govulncheck.sh")),
		fake:   fakeGovulncheck,
		filepath.Join(dir, "SECURITY.md"): "# Security policy\n\nGO-2026-0001 is named here, outside the section.\n\n" +
			"## Known dependency advisories\n\ngovulncheck reports GO-2026-0002, which has no fix the node can take.\n\n" +
			"## Another section\n\nGO-2026-0003 is named here.\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for _, c := range []struct {
		what   string
		status int
		report string
		pass   bool
		says   string
	}{
		{"a clean run", 0, "No vulnerabilities found.", true, ""},
		{"the advisory SECURITY.md explains", 3, govulnReport("GO-2026-0002"), true, ""},
		{"that advisory and another", 3, govulnReport("GO-2026-0002", "GO-2026-0004"), false, "GO-2026-0004 is reachable"},
		{"one SECURITY.md names outside the section", 3, govulnReport("GO-2026-0001"), false, "GO-2026-0001 is reachable"},
		{"one named in the next section", 3, govulnReport("GO-2026-0003"), false, "GO-2026-0003 is reachable"},
		{"govulncheck failing", 1, "govulncheck: loading packages: no Go files", false, "could not finish"},
		{"exit 3 with no advisory to read", 3, "something else entirely", false, "no advisory ID"},
	} {
		cmd := exec.Command("bash", script)
		cmd.Env = append(os.Environ(), "GOVULNCHECK="+fake, "REPORT="+c.report, "STATUS="+strconv.Itoa(c.status), "GITHUB_STEP_SUMMARY=")
		out, err := cmd.CombinedOutput()
		if passed := err == nil; passed != c.pass {
			t.Errorf("%s: passed = %v, want %v\n%s", c.what, passed, c.pass, out)
			continue
		}
		if c.says != "" && !strings.Contains(string(out), c.says) {
			t.Errorf("%s: the output does not say %q:\n%s", c.what, c.says, out)
		}
	}
}

// ruleset is the part of a GitHub ruleset the tests read.
type ruleset struct {
	Target      string `json:"target"`
	Enforcement string `json:"enforcement"`
	Conditions  struct {
		RefName struct {
			Include []string `json:"include"`
		} `json:"ref_name"`
	} `json:"conditions"`
	Rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			RequiredStatusChecks []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
			CodeScanningTools []struct {
				Tool string `json:"tool"`
			} `json:"code_scanning_tools"`
		} `json:"parameters"`
	} `json:"rules"`
}

// TestSecurityChecksGateEveryChange holds CI's security checks to failing
// on what they find, on every pull request and push to main, and main's
// ruleset to requiring them. govulncheck only warned, golangci-lint ran with
// its exit code forced to 0 in a job that could not fail the run, and CodeQL
// ran weekly, on code that had merged up to a week before.
//
// Rules: [RT-32].
func TestSecurityChecksGateEveryChange(t *testing.T) {
	ci := readWorkflow(t, ".github/workflows/ci.yml")
	codeql := readWorkflow(t, ".github/workflows/codeql.yml")
	checks := map[string]bool{}
	for _, w := range []workflow{ci, codeql} {
		if !w.on["pull_request"] || !w.on["push"] || !strings.Contains(w.top["on"], "main") {
			t.Errorf("%s does not run on every pull request and push to main:%s", w.file, w.top["on"])
		}
		for _, j := range w.jobs {
			if v, ok := j.keys["continue-on-error"]; ok && v != "false" {
				t.Errorf("%s: job %s continues on error, so its failure fails nothing", w.file, j.id)
			}
			for _, ignore := range []string{"--issues-exit-code=0", "set +e"} {
				if strings.Contains(j.text, ignore) {
					t.Errorf("%s: job %s ignores a failure with %q", w.file, j.id, ignore)
				}
			}
			checks[j.check()] = true
		}
	}
	ci.job(t, "bash scripts/govulncheck.sh", "runs govulncheck through the script")
	ci.job(t, "golangci/golangci-lint-action@", "runs golangci-lint")
	codeql.job(t, "github/codeql-action/analyze@", "runs CodeQL's analysis")

	const file = ".github/rulesets/main-checks.json"
	var rs ruleset
	if err := json.Unmarshal(read(t, file), &rs); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	if rs.Target != "branch" || rs.Enforcement != "active" || !slices.Equal(rs.Conditions.RefName.Include, []string{"~DEFAULT_BRANCH"}) {
		t.Errorf("%s applies to %s %v, enforcement %q; want the default branch, active", file, rs.Target, rs.Conditions.RefName.Include, rs.Enforcement)
	}
	required, codeScanning := map[string]bool{}, false
	for _, r := range rs.Rules {
		for _, c := range r.Parameters.RequiredStatusChecks {
			required[c.Context] = true
		}
		for _, tool := range r.Parameters.CodeScanningTools {
			codeScanning = codeScanning || (r.Type == "code_scanning" && tool.Tool == "CodeQL")
		}
	}
	for check := range checks {
		if !required[check] {
			t.Errorf("%s does not require %q, which CI runs on pull requests: it can fail and the pull request still merge", file, check)
		}
	}
	for check := range required {
		if !checks[check] {
			t.Errorf("%s requires %q, which no workflow runs on pull requests: every pull request would wait for it", file, check)
		}
	}
	if !codeScanning {
		t.Errorf("%s does not hold merges to CodeQL's alerts (a code_scanning rule for CodeQL)", file)
	}
}

// fakeGH answers each gh call with the files in $GH_RESPONSES in turn, 1, 2,
// ..., repeating the last, and logs its arguments to $GH_LOG.
const fakeGH = `#!/bin/sh
printf '%s\n' "$*" >>"$GH_LOG"
n=$(wc -l <"$GH_LOG")
while [ "$n" -gt 1 ] && [ ! -f "$GH_RESPONSES/$n" ]; do n=$((n - 1)); done
cat "$GH_RESPONSES/$n"
`

// TestReleaseGateNeedsASignedTagAndGreenCI holds the release workflow's gate
// to a tag the installer would build and a commit CI passed: a published
// release went straight to build, push and signing, whatever its tag was and
// whatever CI said of its commit. The gate runs here in a repository of the
// test's own, with the installer's verify_tag pinned to a key of the test's
// own, against a fake gh.
//
// Rules: [RT-33].
func TestReleaseGateNeedsASignedTagAndGreenCI(t *testing.T) {
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
	// The gate reads verify_tag and TAG_SIGNER from scripts/install.sh: here
	// the installer as it is, with the test's key in place of the maintainer's.
	installer := string(read(t, "scripts/install.sh"))
	pinned := regexp.MustCompile(`(?m)^TAG_SIGNER=.*$`)
	if !pinned.MatchString(installer) {
		t.Fatal("scripts/install.sh pins no TAG_SIGNER")
	}
	installer = pinned.ReplaceAllLiteralString(installer, `TAG_SIGNER="`+signer+`"`)
	gate := filepath.Join(repo, "scripts", "release-gate.sh")
	if err := os.MkdirAll(filepath.Dir(gate), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(repo, "scripts", "install.sh"): installer,
		gate:                          string(read(t, "scripts/release-gate.sh")),
		filepath.Join(home, "bin/gh"): fakeGH,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tag := func(name, message, key string) {
		args := []string{"-C", repo, "tag", "-a"}
		if key != "" {
			args = []string{"-C", repo, "-c", "gpg.format=ssh", "-c", "user.signingkey=" + key, "tag", "-s"}
		}
		installerGit(t, home, append(args, "-m", message, name)...)
	}
	tag("v1.0.0", "a release", keys["maintainer"])
	tag("v5.0.0", "not signed", "")
	tag("v6.0.0", "signed with another key", keys["someone"])
	installerGit(t, home, "-C", repo, "update-ref", "refs/tags/v2.0.0", installerGit(t, home, "-C", repo, "rev-parse", "refs/tags/v1.0.0"))
	commit := installerGit(t, home, "-C", repo, "rev-parse", "refs/tags/v1.0.0^{commit}")

	for _, c := range []struct {
		what, tag string
		runs      []string // what CI's runs are, call after call: "status conclusion" a line
		wait      string
		release   bool
		calls     int
	}{
		{"a signed tag, CI passed", "v1.0.0", []string{"completed success"}, "0", true, 1},
		{"a signed tag, CI failed and passed on a re-run", "v1.0.0", []string{"completed failure\ncompleted success"}, "0", true, 1},
		{"a signed tag, CI running, then passed", "v1.0.0", []string{"in_progress ", "completed success"}, "60", true, 2},
		{"a signed tag, CI failed", "v1.0.0", []string{"completed failure"}, "60", false, 1},
		{"a signed tag, CI cancelled", "v1.0.0", []string{"completed cancelled"}, "60", false, 1},
		{"a signed tag, CI still queued at the deadline", "v1.0.0", []string{"queued "}, "0", false, 1},
		{"a signed tag, no CI run", "v1.0.0", []string{""}, "0", false, 1},
		{"the maintainer's v1.0.0 under another name", "v2.0.0", []string{"completed success"}, "0", false, 0},
		{"a tag not signed", "v5.0.0", []string{"completed success"}, "0", false, 0},
		{"a tag signed with another key", "v6.0.0", []string{"completed success"}, "0", false, 0},
		{"no such tag", "v8.0.0", []string{"completed success"}, "0", false, 0},
	} {
		dir := t.TempDir()
		for i, runs := range c.runs {
			if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(i+1)), []byte(runs+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		log := filepath.Join(dir, "log")
		cmd := exec.Command("bash", gate, c.tag)
		cmd.Env = append(installerGitEnv(home), "PATH="+filepath.Join(home, "bin")+":"+os.Getenv("PATH"),
			"GH_LOG="+log, "GH_RESPONSES="+dir, "GH_TOKEN=test", "GITHUB_REPOSITORY=example/dvpnd", "GATE_POLL=0", "GATE_WAIT="+c.wait)
		out, err := cmd.CombinedOutput()
		if released := err == nil; released != c.release {
			t.Errorf("%s (%s): released = %v, want %v\n%s", c.what, c.tag, released, c.release, out)
		}
		logged, _ := os.ReadFile(log)
		var calls []string
		if s := strings.TrimSpace(string(logged)); s != "" {
			calls = strings.Split(s, "\n")
		}
		if len(calls) != c.calls {
			t.Errorf("%s: gh called %d times, want %d: %q", c.what, len(calls), c.calls, calls)
		}
		for _, call := range calls {
			if !strings.Contains(call, "repos/example/dvpnd/actions/workflows/ci.yml/runs?head_sha="+commit+"&") {
				t.Errorf("%s: gh asked %q, want the CI runs of the tagged commit %s", c.what, call, commit)
			}
		}
	}
}

// needs lists the jobs a job needs.
func needs(j workflowJob) []string {
	var list []string
	for _, n := range strings.Split(strings.Trim(j.keys["needs"], "[]"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			list = append(list, n)
		}
	}

	return list
}

// TestReleaseWorkflowRunsTheGateFirst holds the release workflow to its
// gate: a job that runs scripts/release-gate.sh on the release's tag object,
// which every other job needs; and CI to running on each release tag pushed,
// so that the gate has a run of the tagged commit to wait for.
//
// Rules: [RT-33].
func TestReleaseWorkflowRunsTheGateFirst(t *testing.T) {
	w := readWorkflow(t, ".github/workflows/docker-publish.yml")
	if !w.on["release"] {
		t.Errorf("%s does not run on a release:%s", w.file, w.top["on"])
	}
	gate := w.job(t, `bash scripts/release-gate.sh "${TAG}"`, "runs the release gate")
	for _, want := range []string{"TAG: ${{ github.event.release.tag_name }}", `git fetch --force origin "refs/tags/${TAG}:refs/tags/${TAG}"`} {
		if !strings.Contains(gate.text, want) {
			t.Errorf("%s: the gate job lacks %s: verify_tag reads the release's own tag object", w.file, want)
		}
	}
	for _, j := range w.jobs {
		if j.id != gate.id && !slices.Contains(needs(j), gate.id) {
			t.Errorf("%s: job %s does not need the gate (%s), so it runs whatever the tag and CI", w.file, j.id, gate.id)
		}
	}
	ci := readWorkflow(t, ".github/workflows/ci.yml")
	if !regexp.MustCompile(`(?m)^ {4}tags: \['v\*'\]$`).MatchString(ci.top["on"]) {
		t.Errorf("%s does not run on a release tag pushed, so the gate may find no run of the tagged commit:%s", ci.file, ci.top["on"])
	}
}

// TestReleaseCarriesTheSignedInstaller holds the release workflow to
// attaching the installer with a keyless signature, and the operator guide
// to checking it against that workflow: the installer runs as root and came
// only from main, or from a clone checked by hand.
//
// Rules: [RT-34].
func TestReleaseCarriesTheSignedInstaller(t *testing.T) {
	w := readWorkflow(t, ".github/workflows/docker-publish.yml")
	job := w.job(t, "cosign sign-blob --yes --bundle install.sh.sigstore.json install.sh", "signs the installer")
	for _, want := range []string{
		"cp scripts/install.sh install.sh",
		`gh release upload "${TAG}" install.sh install.sh.sigstore.json`,
		"contents: write",
		"id-token: write",
	} {
		if !strings.Contains(job.text, want) {
			t.Errorf("%s: job %s lacks %s", w.file, job.id, want)
		}
	}
	guide := string(read(t, "docs/operator.md"))
	const verify = "cosign verify-blob install.sh --bundle install.sh.sigstore.json"
	i := strings.Index(guide, verify)
	if i < 0 || !strings.Contains(guide[:i], "releases/latest/download") {
		t.Fatalf("docs/operator.md does not check the release's installer with %s", verify)
	}
	command := guide[i:]
	if end := strings.Index(command, "\n```"); end >= 0 {
		command = command[:end]
	}
	identity := `^https://github.com/Feryx-Official/dvpnd/\.github/workflows/` + strings.ReplaceAll(filepath.Base(w.file), ".", `\.`) + `@refs/tags/v`
	for _, want := range []string{"--certificate-identity-regexp '" + identity + "'", "--certificate-oidc-issuer https://token.actions.githubusercontent.com"} {
		if !strings.Contains(command, want) {
			t.Errorf("docs/operator.md checks the release's installer without %s:\n%s", want, command)
		}
	}
}

// TestCIJobsArePinnedAndBounded holds every workflow's jobs to a named
// runner image and a time limit, and its runs to a concurrency group that
// cancels only a pull request's superseded run. ubuntu-latest moved to
// Ubuntu 26.04 under the jobs, which broke the integration suite's tunnels;
// a job that hung ran for GitHub's six hours; a pull request pushed twice
// ran twice to the end.
//
// Rules: [RT-35].
func TestCIJobsArePinnedAndBounded(t *testing.T) {
	image := regexp.MustCompile(`^ubuntu-\d+\.\d+$`)
	for _, file := range files(t, ".github/workflows/*.yml") {
		w := readWorkflow(t, file)
		for _, j := range w.jobs {
			if !image.MatchString(j.keys["runs-on"]) {
				t.Errorf("%s: job %s runs on %q, not a named image", file, j.id, j.keys["runs-on"])
			}
			if n, err := strconv.Atoi(j.keys["timeout-minutes"]); err != nil || n < 1 || n > 60 {
				t.Errorf("%s: job %s has timeout-minutes %q, want 1 to 60", file, j.id, j.keys["timeout-minutes"])
			}
		}
		group := w.top["concurrency"]
		if !strings.Contains(group, "group: ${{ github.workflow }}") {
			t.Errorf("%s has no concurrency group:%s", file, group)
			continue
		}
		cancels := strings.Contains(group, "cancel-in-progress: true")
		ownGroup := strings.Contains(group, "github.event_name == 'pull_request' && github.ref || github.run_id")
		if cancels && !ownGroup {
			t.Errorf("%s cancels more than a pull request's superseded run:%s", file, group)
		}
		if w.on["pull_request"] && !(cancels && ownGroup) {
			t.Errorf("%s runs on pull requests and does not cancel a superseded run:%s", file, group)
		}
	}
}

// TestIssueTemplatesAssignNoOne: the issue templates came from upstream with
// one of its developers as the assignee of every report; a template here
// assigns no one.
func TestIssueTemplatesAssignNoOne(t *testing.T) {
	for _, f := range files(t, ".github/ISSUE_TEMPLATE/") {
		for _, line := range strings.Split(string(read(t, f)), "\n") {
			if v, ok := strings.CutPrefix(line, "assignees:"); ok && strings.Trim(strings.TrimSpace(v), `'"`) != "" {
				t.Errorf("%s assigns %s to every issue it opens", f, strings.TrimSpace(v))
			}
		}
	}
}
