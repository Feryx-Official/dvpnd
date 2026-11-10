// SPDX-License-Identifier: Apache-2.0

package v2ray

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v2raytypes "github.com/Feryx-Official/dvpnd/v9/services/v2ray/types"
)

// initV2Ray runs Init on a configuration with the given VMess port and
// returns the service and the daemon configuration it rendered.
func initV2Ray(t *testing.T, port uint16) (*V2Ray, map[string]interface{}) {
	t.Helper()
	stub(t, "trap 'exit 0' TERM")
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	cfg := v2raytypes.NewConfig().WithDefaultValues()
	cfg.VMess.ListenPort = port
	if err := cfg.SaveToPath(filepath.Join(dir, v2raytypes.ConfigFileName)); err != nil {
		t.Fatal(err)
	}
	s := NewV2Ray()
	if err := s.Init(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, configFileName))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("rendered config is not JSON: %v\n%s", err, raw)
	}

	return s, doc
}

// TestLogsNothing: V2Ray's access log would name every client address and
// destination; the node runs it with every log off.
//
// Rules: [PV-2].
func TestLogsNothing(t *testing.T) {
	_, doc := initV2Ray(t, 8443)
	log, _ := doc["log"].(map[string]interface{})
	for _, k := range []string{"access", "error", "loglevel"} {
		if log[k] != "none" {
			t.Errorf("log.%s = %v, want none", k, log[k])
		}
	}
}

// TestVMessAEADOnly: the daemon starts with v2ray's default, which accepts
// AEAD VMess headers only; the legacy header's MD5 authentication stays off.
//
// Rules: [HS-13].
func TestVMessAEADOnly(t *testing.T) {
	saved, had := os.LookupEnv("V2RAY_VMESS_AEAD_FORCED")
	if err := os.Unsetenv("V2RAY_VMESS_AEAD_FORCED"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("V2RAY_VMESS_AEAD_FORCED", saved)
		}
	})

	seen := filepath.Join(t.TempDir(), "env")
	s := startStub(t, "echo \"aead=${V2RAY_VMESS_AEAD_FORCED-unset}\" > "+seen+"\ntrap 'exit 0' TERM")
	t.Cleanup(func() { _ = s.Stop() })

	raw, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != "aead=unset" {
		t.Fatalf("the daemon started with %s; it must not set V2RAY_VMESS_AEAD_FORCED", got)
	}
}

// TestInfoCarriesThePort: the legacy endpoint hands Info() to old clients,
// which read the listen port from its first two bytes, big-endian.
//
// Rules: [CT-6].
func TestInfoCarriesThePort(t *testing.T) {
	s, _ := initV2Ray(t, 8443)
	if info := s.Info(); len(info) < 2 || binary.BigEndian.Uint16(info) != 8443 {
		t.Fatalf("Info() = %x, want the port 8443 first", info)
	}
}
