// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"text/template"
)

// rename is os.Rename; a test makes it fail.
var rename = os.Rename

// WritePrivateFile writes data to path readable by its owner only. The node's
// configuration files hold keys and passwords, and a file written by an
// earlier release with wider permissions is narrowed too.
//
// The file is replaced, not rewritten: data goes to a new file beside it,
// which is renamed over it once complete, so a crash or a full disk leaves
// the old file whole. A path that is a symlink is written where it points.
// A file mounted on its own (docker run -v file:file) cannot be replaced,
// so it is rewritten in place.
func WritePrivateFile(path string, data []byte) error {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // fails once renamed, as it should

	if err = tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}

	err = rename(tmp.Name(), path)
	if errors.Is(err, syscall.EBUSY) {
		return writeInPlace(path, data)
	}

	return err
}

func writeInPlace(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}

	return os.Chmod(path, 0o600)
}

// TOMLString renders v as a quoted TOML basic string. A JSON string is one
// (its escapes are a subset of TOML's), so a value holding a quote or a
// newline stays one value instead of breaking the file or adding keys to it.
func TOMLString(v interface{}) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(fmt.Sprint(v)); err != nil {
		panic(err) // a string always encodes
	}

	return string(bytes.TrimRight(buf.Bytes(), "\n"))
}

// ConfigTemplate parses a configuration file template with the "toml"
// function, which every string value goes through.
func ConfigTemplate(name, text string) *template.Template {
	return template.Must(template.New(name).Funcs(template.FuncMap{"toml": TOMLString}).Parse(text))
}
