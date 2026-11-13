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
