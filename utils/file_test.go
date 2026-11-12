// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/spf13/viper"
)

// TestWritePrivateFile: a file the node writes is readable by its owner
// only, a file an earlier release left readable by others included, and so
// is a file rewritten in place because it cannot be replaced.
//
// Rules: [PV-6].
func TestWritePrivateFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		inUse bool
	}{{name: "replaced"}, {name: "rewritten in place", inUse: true}} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.inUse {
				failRename(t)
			}
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := WritePrivateFile(path, []byte("new")); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("mode %o, want 600", info.Mode().Perm())
			}
			if got, _ := os.ReadFile(path); string(got) != "new" {
				t.Fatalf("read back %q", got)
			}
		})
	}
}

// failRename makes every rename fail as it does on a file mounted on its
// own (docker run -v file:file), which can only be rewritten in place.
func failRename(t *testing.T) {
	t.Helper()
	saved := rename
	rename = func(string, string) error { return &os.LinkError{Op: "rename", Err: syscall.EBUSY} }
	t.Cleanup(func() { rename = saved })
}

// TestWritePrivateFileReplacesTheFile: a configuration file is replaced by
// a complete new one, never truncated and rewritten, so a crash or a full
// disk while writing leaves the old file whole. A second name for the old
// file still reads the old content afterwards, which an in-place rewrite
// would have changed. Nothing is left beside the file, and a file reached
// through a symlink is written where the link points, the link kept.
//
// Rules: [RT-20].
func TestWritePrivateFileReplacesTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(dir, "second-name")); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivateFile(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("read back %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "second-name")); string(got) != "old" {
		t.Fatalf("the old file was rewritten in place: it now reads %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("files left beside the configuration: %v", entries)
	}

	target := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "config.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivateFile(link, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink was replaced: %v, %v", info, err)
	}
	if got, _ := os.ReadFile(target); string(got) != "new" {
		t.Fatalf("the link's target reads %q", got)
	}
}

// TestTOMLStringKeepsAValueOneValue: whatever an operator passes to
// "config set", it reads back as the same single value.
//
// Rules: [PV-6].
func TestTOMLStringKeepsAValueOneValue(t *testing.T) {
	for _, v := range []string{
		"plain",
		`with "quotes"`,
		"multi\nline = \"injected\"\n[section]",
		`back\slash`,
		"https://example.com/path?a=1&b=<2>",
		"tab\there",
	} {
		var buf bytes.Buffer
		tmpl := ConfigTemplate("t", "key = {{ toml .V }}\nother = 1\n")
		if err := tmpl.Execute(&buf, struct{ V string }{v}); err != nil {
			t.Fatal(err)
		}

		r := viper.New()
		r.SetConfigType("toml")
		if err := r.ReadConfig(&buf); err != nil {
			t.Fatalf("%q: rendered file does not parse: %v\n%s", v, err, buf.String())
		}
		if got := r.GetString("key"); got != v {
			t.Errorf("read back %q, want %q", got, v)
		}
		if keys := r.AllKeys(); len(keys) != 2 {
			t.Errorf("%q added keys: %v", v, keys)
		}
	}
	if got := TOMLString("a&b"); got != `"a&b"` {
		t.Errorf("TOMLString escapes HTML: %s", got)
	}
}
