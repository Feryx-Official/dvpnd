// SPDX-License-Identifier: Apache-2.0

package static

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/types/bech32"
)

const (
	spdxText     = "SPDX-License-Identifier: Apache-2.0"
	modifiedText = "Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE."
	spdxLine     = "// " + spdxText
	modifiedLine = "// " + modifiedText
	copyright    = "Copyright [2017] [Sentinel]"
	sdkModule    = "github.com/sentinel-official/sentinel-go-sdk"
	// The module path at the fork point and now; renaming it alone is not a
	// change to a file.
	upstreamModule = "github.com/sentinel-official/dvpn-node"
	forkModule     = "github.com/" + accountName + "/dvpnd/v9"
	// accountName is the GitHub account that owns the repository.
	accountName = "Feryx-Official"
)

// formerNames are where the repository lived before, in lower case: an account
// name anyone can register once it is given up, and the path a transfer left
// behind, which stops forwarding once a repository is created there.
var formerNames = []string{"trinitystake", "proofoftrinity/dvpnd"}

// formerNameAllowed lists the lines that may still name a former home,
// keyed by file and line (spaces collapsed), each with its reason: they record
// the past and point nobody anywhere.
var formerNameAllowed = map[string]string{
	"docs/provenance/fork-verification.txt: PASS fork-point tag signature verifies (trinitystake <114076168+trinitystake@users.noreply.github.com>)": "a dated record of a provenance run; the fork-point tag was signed under that name",
	"README.md: then: `github.com/proofoftrinity/dvpnd/v9` from 9.4.1, `github.com/trinitystake/dvpnd/v9`":                                           "the module paths the releases before the move declare",
	`invariants/canaries.json: "replace": "REPO_URL=\"https://github.com/trinitystake/dvpnd.git\"",`:                                                 "the canary puts the old name back to prove this test catches it",
	`invariants/canaries.json: "replace": "REPO_URL=\"https://github.com/proofoftrinity/dvpnd.git\"",`:                                               "the canary puts the old path back to prove this test catches it",
	"docs/operator.md: them from `ghcr.io/proofoftrinity/dvpnd`, and to check one, write `proofoftrinity` for":                                       "where the images built before the move are published",
	"docs/operator.md: `Feryx-Official` in the identity and the `--repo` value (`trinitystake` for 9.4.0, signed":                                    "the identity that signed the images built before the rename",
}

// Rules: [LIC-1].
func TestEveryGoFileStartsWithSPDX(t *testing.T) {
	for _, f := range files(t, "*.go") {
		first, _, _ := strings.Cut(string(read(t, f)), "\n")
		if first != spdxLine {
			t.Errorf("%s: the first line must be %q", f, spdxLine)
		}
	}
}

// hashCommented are the files besides Go that are code or configuration and
// take "#" comments.
var hashCommented = []string{"*.sh", "*.py", "*Dockerfile", "Makefile", "*.yml", "*.yaml", "*.service", ".gitignore", ".dockerignore"}

// Rules: [LIC-1].
func TestEveryScriptAndConfigFileStartsWithSPDX(t *testing.T) {
	for _, f := range files(t, hashCommented...) {
		lines := strings.SplitN(string(read(t, f)), "\n", 3)
		if strings.HasPrefix(lines[0], "#!") {
			lines = lines[1:]
		}
		if lines[0] != "# "+spdxText {
			t.Errorf("%s: the first line, or the second after a #! line, must be %q", f, "# "+spdxText)
		}
	}
}

// Rules: [LIC-2].
func TestNoSentinelGoSDK(t *testing.T) {
	for _, f := range []string{"go.mod", "go.sum"} {
		if bytes.Contains(read(t, f), []byte(sdkModule)) {
			t.Errorf("%s names %s, which ships no licence (all rights reserved)", f, sdkModule)
		}
	}
	fset := token.NewFileSet()
	for _, f := range files(t, "*.go") {
		file, err := parser.ParseFile(fset, filepath.Join(root(t), f), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); strings.HasPrefix(path, sdkModule) {
				t.Errorf("%s imports %s", f, path)
			}
		}
	}
}

// knownUnmarked lists upstream files changed in substance that do not say so
// yet. Each entry is a known bug waiting for its fix; the test fails once an
// entry is no longer true, so the list can only shrink.
var knownUnmarked = map[string]string{}

// unmarkable lists the upstream files changed here that carry no notice, each
// with its reason.
var unmarkable = map[string]string{
	"go.sum":    "Go's checksum list takes no comments; NOTICE says it changed",
	"README.md": "rewritten whole: no line of upstream's README is left",
}

// commentText is the text of a "//", "#" or "<!-- -->" comment line, or ""
// when the line is not one.
func commentText(line string) string {
	line = strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(line, "//"):
		return strings.TrimSpace(line[2:])
	case strings.HasPrefix(line, "#"):
		return strings.TrimSpace(line[1:])
	case strings.HasPrefix(line, "<!--") && strings.HasSuffix(line, "-->"):
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "<!--"), "-->"))
	}

	return ""
}

// saysModified reports whether a file carries the notice as a comment in any
// of the syntaxes commentText reads.
func saysModified(src string) bool {
	for _, line := range strings.Split(src, "\n") {
		if commentText(line) == modifiedText {
			return true
		}
	}

	return false
}

// substance is a file's content as a multiset of lines, ignoring what does
// not change what the file says: the licence header lines in any comment
// syntax, blank lines, whitespace inside a line, the order of lines, and the
// module path.
func substance(src string, upstream bool) map[string]int {
	lines := map[string]int{}
	for _, line := range strings.Split(src, "\n") {
		if upstream {
			line = strings.ReplaceAll(line, upstreamModule, forkModule)
		}
		line = strings.Join(strings.Fields(line), " ")
		if c := commentText(line); line == "" || c == spdxText || c == modifiedText {
			continue
		}
		lines[line]++
	}

	return lines
}

func sameSubstance(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for line, n := range a {
		if b[line] != n {
			return false
		}
	}

	return true
}

// Rules: [LIC-3].
func TestChangedUpstreamFilesSayModified(t *testing.T) {
	upstream := map[string]bool{}
	for _, f := range strings.Split(string(git(t, "ls-tree", "-r", "-z", "--name-only", "fork-point")), "\x00") {
		upstream[f] = f != ""
	}
	if len(upstream) == 0 {
		t.Fatal("the fork-point tag lists no files; fetch the tags (git fetch --tags)")
	}

	for _, f := range files(t, ".") {
		if !upstream[f] {
			continue
		}
		if _, ok := unmarkable[f]; ok {
			continue
		}
		current := string(read(t, f))
		changed := !sameSubstance(substance(string(git(t, "show", "fork-point:"+f)), true), substance(current, false))
		marked := saysModified(current)
		if strings.HasSuffix(f, ".go") {
			marked = strings.Contains(current, modifiedLine)
		}
		_, known := knownUnmarked[f]
		switch {
		case changed && !marked && !known:
			t.Errorf("%s is changed from upstream: add %q as a comment under the SPDX line, or at the top where it has none", f, modifiedText)
		case known && (marked || !changed):
			t.Errorf("%s carries the notice now (or matches upstream): remove it from knownUnmarked", f)
		}
	}
	for _, list := range []map[string]string{knownUnmarked, unmarkable} {
		for f := range list {
			if _, err := os.Stat(filepath.Join(root(t), f)); err != nil || !upstream[f] {
				t.Errorf("%s is listed but is not an upstream file in the tree; remove it", f)
			}
		}
	}
}

// Rules: [LIC-4].
func TestLicenseIsTheForkPoints(t *testing.T) {
	if !bytes.Equal(read(t, "LICENSE"), git(t, "show", "fork-point:LICENSE")) {
		t.Error("LICENSE differs from the fork point's; it is never edited")
	}
	for _, f := range []string{"LICENSE", "NOTICE"} {
		if !bytes.Contains(read(t, f), []byte(copyright)) {
			t.Errorf("%s lost the line %q", f, copyright)
		}
	}
}

// TestProvenanceRecordVerifies runs the provenance script offline: a curl that
// always fails stands in for the network, so the outside attestations (the
// script reports them as info) are skipped and every check on the repository
// itself runs.
//
// Rules: [LIC-5].
func TestProvenanceRecordVerifies(t *testing.T) {
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "curl"), []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "docs/provenance/verify-fork.sh")
	cmd.Dir = root(t)
	cmd.Env = append(os.Environ(), "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("RESULT: all checks passed")) {
		t.Fatalf("verify-fork.sh: %v\n%s", err, out)
	}
	if bytes.Contains(out, []byte("PASS  apache.org")) {
		t.Fatal("the script reached the network past the curl stub")
	}
}

// archiveCurl stands in for curl with one site reachable, Software Heritage,
// which answers with the HTTP status in ARCHIVE_STATUS: a revision record for
// the commit asked about on 200, an error page otherwise. It honours -o, -w
// and -f as curl does; every other address fails to connect.
const archiveCurl = `#!/bin/sh
out=; fmt=; url=; fail=0
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift 2 ;;
    -w) fmt=$2; shift 2 ;;
    --*) shift ;;
    -*) case "$1" in *f*) fail=1 ;; esac; shift ;;
    *) url=$1; shift ;;
  esac
done
case "$url" in
  https://archive.softwareheritage.org/*) ;;
  *) exit 7 ;;
esac
[ "$fail" = 1 ] && [ "$ARCHIVE_STATUS" -ge 400 ] && exit 22
id=${url%/}; id=${id##*/}
case "$ARCHIVE_STATUS" in
  200) page="{\"id\":\"$id\",\"date\":\"2024-01-25T00:00:00+00:00\",\"message\":\"a commit\"}" ;;
  404) page='{"exception":"NotFoundExc","reason":"Revision not found"}' ;;
  *) page="<html><body><h1>$ARCHIVE_STATUS</h1>The server is temporarily unable to serve your request.</body></html>" ;;
esac
if [ -n "$out" ]; then printf '%s' "$page" >"$out"; else printf '%s' "$page"; fi
[ -z "$fmt" ] || printf '%s' "$ARCHIVE_STATUS"
`

// TestProvenanceCheckTellsAnArchiveDownFromACommitMissing holds the provenance
// script to failing on the archive's answer, not on the archive being down: an
// error page Software Heritage served while unavailable failed make check, the
// commit hook and CI. A 404 is an answer and still fails.
//
// Rules: [LIC-5].
func TestProvenanceCheckTellsAnArchiveDownFromACommitMissing(t *testing.T) {
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "curl"), []byte(archiveCurl), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		status string
		pass   bool
		says   string
	}{
		{"200", true, "PASS  Software Heritage archives"},
		{"503", true, "info  Software Heritage unreachable"},
		{"502", true, "info  Software Heritage unreachable"},
		{"429", true, "info  Software Heritage rate-limited"},
		{"404", false, "FAIL  Software Heritage does not archive"},
	} {
		cmd := exec.Command("bash", "docs/provenance/verify-fork.sh")
		cmd.Dir = root(t)
		cmd.Env = append(os.Environ(), "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"),
			"GIT_OPTIONAL_LOCKS=0", "ARCHIVE_STATUS="+c.status)
		out, err := cmd.CombinedOutput()
		if passed := err == nil && bytes.Contains(out, []byte("RESULT: all checks passed")); passed != c.pass {
			t.Errorf("with Software Heritage answering %s the script passed = %v, want %v (%v)", c.status, passed, c.pass, err)
		}
		if !bytes.Contains(out, []byte(c.says)) {
			t.Errorf("with Software Heritage answering %s the script never says %q:\n%s", c.status, c.says, out)
		}
	}
}

// bech32Candidate matches what could be a Sentinel account, node or
// validator address; the checksum decides.
var bech32Candidate = regexp.MustCompile(`\bsent[a-z]*1[02-9ac-hj-np-z]{6,}\b`)

// addressAllowed lists addresses that may appear, each with its reason.
var addressAllowed = map[string]string{}

// addresses returns the checksum-valid 20- or 32-byte Sentinel addresses in
// text.
func addresses(text string) []string {
	var found []string
	for _, s := range bech32Candidate.FindAllString(text, -1) {
		if _, data, err := bech32.DecodeAndConvert(s); err == nil && (len(data) == 20 || len(data) == 32) {
			found = append(found, s)
		}
	}

	return found
}

// Rules: [LIC-6].
func TestNoOnChainAddresses(t *testing.T) {
	// Positive control: an address made here is found, one with a broken
	// checksum is not, so a scanner that finds nothing cannot pass.
	made, err := bech32.ConvertAndEncode("sentnode", bytes.Repeat([]byte{7}, 20))
	if err != nil {
		t.Fatal(err)
	}
	broken := made[:len(made)-1] + map[bool]string{true: "q", false: "p"}[made[len(made)-1] != 'q']
	if got := addresses("node " + made + " and " + broken); len(got) != 1 || got[0] != made {
		t.Fatalf("the scanner found %v in a text holding one valid address", got)
	}

	var hits []string
	for _, f := range files(t, ".") {
		b := read(t, f)
		if bytes.IndexByte(b, 0) >= 0 { // binary
			continue
		}
		for _, a := range addresses(string(b)) {
			if _, ok := addressAllowed[a]; !ok {
				hits = append(hits, f+": "+a)
			}
		}
	}
	sort.Strings(hits)
	for _, h := range hits {
		t.Errorf("an on-chain address in a committed file (it links the repository to a wallet): %s", h)
	}
}

// Rules: [LIC-8].
func TestNoFormerAccountName(t *testing.T) {
	if module, _, _ := strings.Cut(string(read(t, "go.mod")), "\n"); module != "module "+forkModule {
		t.Errorf("go.mod declares %q; the module path is %s", module, forkModule)
	}

	_, self, _, _ := runtime.Caller(0)
	self = "test/static/" + filepath.Base(self) // it names them to look for them
	seen := map[string]bool{}
	for _, f := range files(t, ".") {
		b := read(t, f)
		if f == self || bytes.IndexByte(b, 0) >= 0 { // binary
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, name := range formerNames {
				if !strings.Contains(strings.ToLower(line), name) {
					continue
				}
				key := f + ": " + strings.Join(strings.Fields(line), " ")
				if _, ok := formerNameAllowed[key]; ok {
					seen[key] = true
					continue
				}
				t.Errorf("%s:%d names %s, a former home of the repository; use the account %s, since a former home serves whatever is put there next", f, i+1, name, accountName)
			}
		}
	}
	for key := range formerNameAllowed {
		if !seen[key] {
			t.Errorf("formerNameAllowed lists a line that is no longer in the tree; remove it: %s", key)
		}
	}
}

// TestSubstance holds the comparison above to known changes: the licence
// header and the module rename are not a change, anything else is.
func TestSubstance(t *testing.T) {
	upstream := "package x\n\nimport \"" + upstreamModule + "/types\"\n\nfunc F() {}\n"
	for _, c := range []struct {
		name, current string
		changed       bool
	}{
		{"header and module rename only", spdxLine + "\n\npackage x\n\nimport \"" + forkModule + "/types\"\n\nfunc F() {}\n", false},
		{"reformatted", spdxLine + "\npackage x\nimport   \"" + forkModule + "/types\"\nfunc F()   {}\n", false},
		{"a changed line", spdxLine + "\n\npackage x\n\nimport \"" + forkModule + "/types\"\n\nfunc F() { G() }\n", true},
		{"an added line", spdxLine + "\n\npackage x\n\nimport \"" + forkModule + "/types\"\n\nfunc F() {}\n\nfunc G() {}\n", true},
	} {
		if got := !sameSubstance(substance(upstream, true), substance(c.current, false)); got != c.changed {
			t.Errorf("%s: changed = %v, want %v", c.name, got, c.changed)
		}
	}

	script := "#!/bin/sh\nset -e\necho hi\n"
	for _, c := range []struct {
		name, current string
		changed       bool
	}{
		{"# header only", "#!/bin/sh\n# " + spdxText + "\n# " + modifiedText + "\nset -e\necho hi\n", false},
		{"<!-- --> header only", "<!-- " + modifiedText + " -->\n#!/bin/sh\nset -e\necho hi\n", false},
		{"a changed line", "#!/bin/sh\n# " + spdxText + "\nset -eu\necho hi\n", true},
	} {
		if got := !sameSubstance(substance(script, true), substance(c.current, false)); got != c.changed {
			t.Errorf("%s: changed = %v, want %v", c.name, got, c.changed)
		}
	}
	for src, want := range map[string]bool{
		"# " + modifiedText:                  true,
		"x\n  <!-- " + modifiedText + " -->": true,
		"// " + modifiedText:                 true,
		"x " + modifiedText:                  false,
	} {
		if saysModified(src) != want {
			t.Errorf("saysModified(%q) = %v, want %v", src, !want, want)
		}
	}
}
