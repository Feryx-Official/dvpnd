// SPDX-License-Identifier: Apache-2.0

package static

import (
	"path"
	"regexp"
	"strings"
	"testing"
)

// shellValues returns the values the lines assign with NAME=value (shell) or
// ARG NAME=value (Dockerfile), unquoted, by name.
func shellValues(text string) map[string]string {
	values := map[string]string{}
	re := regexp.MustCompile(`^(?:ARG\s+)?([A-Z][A-Z0-9_]*)=("([^"]*)"|(\S*))$`)
	for _, line := range strings.Split(text, "\n") {
		if m := re.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			values[m[1]] = m[3] + m[4]
		}
	}

	return values
}

// TestImageAndInstallerPinTheSameDaemons holds the node image and the
// installer to the same protocol binaries, each pinned in full: the versions
// were written twice by hand, so a bump in one left a host node and a Docker
// node running different daemons, and the image checked hnsd's commit by a
// 12-character prefix.
//
// Rules: [RT-30].
func TestImageAndInstallerPinTheSameDaemons(t *testing.T) {
	dockerfile := string(read(t, "Dockerfile"))
	image, installer := shellValues(dockerfile), shellValues(string(read(t, "scripts/install.sh")))
	shared := 0
	for name, value := range image {
		theirs, ok := installer[name]
		if !ok {
			continue
		}
		shared++
		if theirs != value {
			t.Errorf("Dockerfile pins %s=%s, scripts/install.sh %s=%s", name, value, name, theirs)
		}
	}
	if shared < 8 {
		t.Errorf("Dockerfile and scripts/install.sh share %d pins, want xray's, Hysteria's and AmneziaWG's 8", shared)
	}

	sha := regexp.MustCompile(`^[0-9a-f]{64}$`)
	commit := regexp.MustCompile(`^[0-9a-f]{40}$`)
	for name, value := range image {
		if strings.HasSuffix(name, "_SHA256") && !sha.MatchString(value) {
			t.Errorf("Dockerfile pins %s to %q, not a sha256", name, value)
		}
		if strings.HasSuffix(name, "_COMMIT") && !commit.MatchString(value) {
			t.Errorf("Dockerfile pins %s to %q, not a full commit hash", name, value)
		}
	}
	checks := regexp.MustCompile(`rev-parse HEAD \| grep -q\S* \S*?([0-9a-f]+|\$\{[A-Z_]+\})`)
	found := 0
	for i, line := range strings.Split(dockerfile, "\n") {
		m := checks.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		found++
		value := m[1]
		if strings.HasPrefix(value, "${") {
			value = image[strings.Trim(value, "${}")]
		}
		if !commit.MatchString(value) {
			t.Errorf("Dockerfile:%d checks a clone against %q, not a full commit hash: %s", i+1, m[1], strings.TrimSpace(line))
		}
	}
	if found < 3 {
		t.Errorf("Dockerfile checks %d clones' commits, want hnsd's and AmneziaWG's two", found)
	}
}

// TestBaseImagesArePinned holds every Dockerfile in the tree to base images
// pinned by digest, which Dependabot moves, and to one Alpine release across
// a Dockerfile's stages. The node image built hnsd on Alpine 3.23 and ran it
// on 3.24; the AmneziaWG client check and the systemd test host took
// whatever their tags pointed at, outside the one directory Dependabot
// watched.
//
// Rules: [RT-30].
func TestBaseImagesArePinned(t *testing.T) {
	from := regexp.MustCompile(`^FROM\s+(\S+)(?:\s+AS\s+(\S+))?`)
	pinned := regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
	alpine := regexp.MustCompile(`(?:-alpine|^alpine:)(\d+\.\d+)@`)
	dirs := map[string]bool{}
	for _, file := range files(t, "*Dockerfile*") {
		dirs["/"+strings.TrimPrefix(path.Dir(file), ".")] = true
		stages := map[string]bool{}
		releases := map[string]bool{}
		for i, line := range strings.Split(string(read(t, file)), "\n") {
			m := from.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				continue
			}
			base := m[1]
			if m[2] != "" {
				stages[m[2]] = true
			}
			if strings.HasPrefix(base, "$") || stages[base] {
				continue
			}
			if !pinned.MatchString(base) {
				t.Errorf("%s:%d builds on %s, not pinned by digest", file, i+1, base)
			}
			if r := alpine.FindStringSubmatch(base); r != nil {
				releases[r[1]] = true
			}
		}
		if len(releases) > 1 {
			t.Errorf("%s builds on Alpine releases %v, want one", file, releases)
		}
	}

	config := string(read(t, ".github/dependabot.yml"))
	start := strings.Index(config, "package-ecosystem: docker")
	if start < 0 {
		t.Fatal(".github/dependabot.yml has no docker entry")
	}
	entry := config[start:]
	if next := strings.Index(entry, "- package-ecosystem:"); next >= 0 {
		entry = entry[:next]
	}
	watched := map[string]bool{}
	if m := regexp.MustCompile(`(?m)^\s*directory:\s*(\S+)`).FindStringSubmatch(entry); m != nil {
		watched[strings.Trim(m[1], `"'`)] = true
	}
	if m := regexp.MustCompile(`directories:\s*\[([^\]]*)\]`).FindStringSubmatch(entry); m != nil {
		for _, d := range strings.Split(m[1], ",") {
			watched[strings.Trim(strings.TrimSpace(d), `"'`)] = true
		}
	}
	for dir := range dirs {
		if !watched[dir] {
			t.Errorf("Dependabot's docker entry does not watch %s, which holds a Dockerfile", dir)
		}
	}
}

// TestOneGoToolchain holds CI, the installer and the node image to one Go
// toolchain: go.mod named only the minimum language version, so CI tested
// and raced on 1.26.10, the installer built nodes with 1.26.10, which
// carries a reachable net/http advisory, and the image was compiled with
// 1.27.4. go.mod's toolchain line, the image's Go and the Go the installer
// installs are the same release.
//
// Rules: [RT-31].
func TestOneGoToolchain(t *testing.T) {
	var toolchain string
	for _, line := range strings.Split(string(read(t, "go.mod")), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "toolchain go"); ok {
			toolchain = v
		}
	}
	if toolchain == "" {
		t.Fatal("go.mod names no toolchain: CI and an installer build take whatever Go they find")
	}
	installer := shellValues(string(read(t, "scripts/install.sh")))["GO_VERSION"]
	if installer != toolchain {
		t.Errorf("scripts/install.sh installs Go %q, go.mod's toolchain is %s", installer, toolchain)
	}
	m := regexp.MustCompile(`(?m)^FROM golang:(\d+\.\d+\.\d+)-`).FindStringSubmatch(string(read(t, "Dockerfile")))
	if m == nil {
		t.Fatal("the Dockerfile builds on no golang image of an exact release")
	}
	if m[1] != toolchain {
		t.Errorf("the image is built with Go %s, go.mod's toolchain is %s", m[1], toolchain)
	}
}
