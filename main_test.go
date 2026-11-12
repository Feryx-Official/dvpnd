// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Feryx-Official/dvpnd/v9/services"
)

// mainArgs carries the arguments of a dvpnd run from a test to the child it
// starts, separated by newlines.
const mainArgs = "DVPND_TEST_MAIN_ARGS"

// TestMain lets a test run dvpnd itself: started with mainArgs set, the test
// binary runs main with those arguments and exits as the Go runtime does when
// main returns.
func TestMain(m *testing.M) {
	if args, ok := os.LookupEnv(mainArgs); ok {
		os.Args = append([]string{"dvpnd"}, strings.Split(args, "\n")...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runDvpnd runs dvpnd with args in a child process and returns its exit
// status and output.
func runDvpnd(t *testing.T, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), mainArgs+"="+strings.Join(args, "\n"), "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &exit):
		return exit.ExitCode(), string(out)
	default:
		t.Fatalf("running dvpnd %v: %v", args, err)
		return 0, ""
	}
}

// TestFailingCommandExitsNonZero holds dvpnd to the exit status a failure
// must have: the installer runs under set -e, and systemd and Docker read the
// status. A command that succeeds is the control: the child runs dvpnd.
//
// Rules: [RT-10].
func TestFailingCommandExitsNonZero(t *testing.T) {
	if status, out := runDvpnd(t, "version"); status != 0 {
		t.Fatalf("dvpnd version exited %d:\n%s", status, out)
	}

	home := t.TempDir() // no config.toml in it
	for _, args := range [][]string{
		{"start", "--home", home},
		{"config", "set", "node.moniker", "--home", home},
	} {
		status, out := runDvpnd(t, args...)
		if !strings.Contains(out, "Error:") {
			t.Errorf("dvpnd %v did not fail as expected; output:\n%s", args, out)
		}
		if status == 0 {
			t.Errorf("dvpnd %v failed (%s) yet exited 0", args, strings.TrimSpace(out))
		}
	}
}

// setKey is a key a script sets with config set, and the line that sets it.
type setKey struct{ key, at string }

// setValue is what TestConfigSetRefusesAnUnknownKey sets a key to when "1"
// is not a value config set accepts for it; every other key gets "1".
var setValue = map[string]string{
	"vless.security": "tls", // tls or reality only
}

// installerSet matches a config set in the installer: the protocol named, or
// the node type in a case arm, or none for the node's own file; then the key.
// runnerSet matches one in the Docker runner, through its <protocol>_config_set
// functions or config_set for the node's own file.
var (
	installerSet = regexp.MustCompile(`\bdv (?:("\$\{NODE_TYPE\}"|[a-z0-9]+) )?config set ([a-z0-9_.]+) `)
	caseArm      = regexp.MustCompile(`^\s*([a-z0-9|]+)\)`)
	runnerSet    = regexp.MustCompile(`^\s*(?:([a-z0-9]+)_)?config_set "([a-z0-9_.]+)"`)
)

// scriptKeys returns the keys the installer and the Docker runner set, by the
// protocol whose file they are in, "" for the node's own.
func scriptKeys(t *testing.T) map[string][]setKey {
	t.Helper()
	keys := map[string][]setKey{}
	lines := func(file string) []string {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}

		return strings.Split(string(b), "\n")
	}

	const installer = "scripts/install.sh"
	var arm []string
	for i, line := range lines(installer) {
		if m := caseArm.FindStringSubmatch(line); m != nil {
			arm = strings.Split(m[1], "|")
		} else if strings.TrimSpace(line) == "esac" {
			arm = nil
		}
		m := installerSet.FindStringSubmatch(line)
		at := fmt.Sprintf("%s:%d", installer, i+1)
		switch {
		case m == nil:
		case m[1] != `"${NODE_TYPE}"`:
			keys[m[1]] = append(keys[m[1]], setKey{m[2], at})
		case arm == nil:
			t.Fatalf("%s sets %s for the node type outside a case arm naming it", at, m[2])
		default:
			for _, name := range arm {
				keys[name] = append(keys[name], setKey{m[2], at})
			}
		}
	}

	const runner = "scripts/runner.sh"
	for i, line := range lines(runner) {
		if m := runnerSet.FindStringSubmatch(line); m != nil {
			keys[m[1]] = append(keys[m[1]], setKey{m[2], fmt.Sprintf("%s:%d", runner, i+1)})
		}
	}

	return keys
}

// files returns the contents of every file under dir by its path.
func files(t *testing.T, dir string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		got[path] = string(b)

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	return got
}

// TestConfigSetRefusesAnUnknownKey holds config set to refusing a key its
// file does not have: viper took any key, the file was saved without it and
// the command exited 0, so a misspelt key was lost without a word. It checks
// the node's file and every protocol's: a key that does not exist, one under
// a table that does not exist, and one misspelt. The keys the installer and
// the Docker runner set are the control: each is accepted.
//
// Rules: [RT-13].
func TestConfigSetRefusesAnUnknownKey(t *testing.T) {
	keys := scriptKeys(t)
	configs := []string{""}
	for _, p := range services.All() {
		configs = append(configs, p.Name)
	}
	for _, name := range configs {
		file := name
		if file == "" {
			file = "node"
		}
		t.Run(file, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			dvpnd := func(args ...string) (int, string) {
				if name != "" {
					args = append([]string{name}, args...)
				}

				return runDvpnd(t, append(args, "--home", home)...)
			}
			if status, out := dvpnd("config", "init"); status != 0 {
				t.Fatalf("config init exited %d:\n%s", status, out)
			}
			known := keys[name]
			if len(known) == 0 {
				t.Fatalf("neither the installer nor the runner sets a key in this file, so no key is known to be accepted")
			}

			before := files(t, home)
			for _, key := range []string{"no_such_key", "no_such_table.key", known[0].key + "x"} {
				status, out := dvpnd("config", "set", key, "1")
				if status == 0 || !strings.Contains(out, "Error:") || !strings.Contains(out, key) {
					t.Errorf("config set %s 1 exited %d; want an error naming the key:\n%s", key, status, out)
				}
			}
			if !maps.Equal(files(t, home), before) {
				t.Error("a refused config set changed the files")
			}

			for _, k := range known {
				value := "1"
				if v, ok := setValue[k.key]; ok {
					value = v
				}
				if status, out := dvpnd("config", "set", k.key, value); status != 0 {
					t.Errorf("config set %s %s, as %s does, exited %d:\n%s", k.key, value, k.at, status, out)
				}
			}
			if name == "" {
				dvpnd("config", "set", "node.moniker", "set-by-test")
				if _, out := dvpnd("config", "show"); !strings.Contains(out, "set-by-test") {
					t.Errorf("config set node.moniker did not store the value; config show:\n%s", out)
				}
			}
		})
	}
}

// TestXRayConfigSetRefusesCleartext holds xray config set to refusing a
// vless.security the node would refuse at start. VLESS has no cipher of its
// own, so any value but tls or reality would leave the proxy hop cleartext;
// saved, it surfaced only at the next start, after the installer had moved
// on. The two values the node accepts are the control.
//
// Rules: [PV-9].
func TestXRayConfigSetRefusesCleartext(t *testing.T) {
	home := t.TempDir()
	xray := func(args ...string) (int, string) {
		return runDvpnd(t, append(append([]string{"xray"}, args...), "--home", home)...)
	}
	if status, out := xray("config", "init"); status != 0 {
		t.Fatalf("xray config init exited %d:\n%s", status, out)
	}

	before := files(t, home)
	for _, value := range []string{"none", ""} {
		status, out := xray("config", "set", "vless.security", value)
		if status == 0 || !strings.Contains(out, "Error:") || !strings.Contains(out, "security must be") {
			t.Errorf("xray config set vless.security %q exited %d; want the security error:\n%s", value, status, out)
		}
	}
	if !maps.Equal(files(t, home), before) {
		t.Error("a refused xray config set changed the files")
	}

	for _, value := range []string{"reality", "tls"} {
		if status, out := xray("config", "set", "vless.security", value); status != 0 {
			t.Errorf("xray config set vless.security %s exited %d:\n%s", value, status, out)
		}
	}
	b, err := os.ReadFile(filepath.Join(home, "xray.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `security = "tls"`) {
		t.Errorf("xray config set vless.security tls did not store the value:\n%s", b)
	}
}
