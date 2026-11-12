// SPDX-License-Identifier: Apache-2.0

package v2ray

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
	v2raytypes "github.com/Feryx-Official/dvpnd/v9/services/v2ray/types"
)

// stub writes an executable script named v2ray into a temp dir and points
// binaryName at it. The script's body decides how it reacts to SIGTERM.
// It returns the path of a file the script creates once its trap is
// installed, so a test can wait for the child to be ready before signalling.
func stub(t *testing.T, body string) string {
	t.Helper()

	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	path := filepath.Join(dir, "v2ray")
	script := "#!/bin/sh\n" + body + "\ntouch " + ready + "\nwhile :; do sleep 0.1; done\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	saved := binaryName
	binaryName = path
	t.Cleanup(func() { binaryName = saved })

	return ready
}

func startStub(t *testing.T, body string) *V2Ray {
	t.Helper()

	ready := stub(t, body)
	s := NewV2Ray()
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatal("stub never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Rules: [RT-6].
func TestInitRequiresBinaryOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	saved := binaryName
	binaryName = "v2ray"
	t.Cleanup(func() { binaryName = saved })

	err := NewV2Ray().Init(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), `"v2ray" binary is not on PATH`) {
		t.Fatalf("Init without the binary: %v", err)
	}
}

// Rules: [RT-3].
func TestStopTerminatesChild(t *testing.T) {
	s := startStub(t, "trap 'exit 0' TERM")

	start := time.Now()
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("Stop took %s, the child should have exited on SIGTERM", time.Since(start))
	}
	if exited, state := s.process.Exited(); !exited || !state.Exited() {
		t.Fatal("child was not reaped")
	}
}

// Rules: [RT-3].
func TestStopKillsChildThatIgnoresTerm(t *testing.T) {
	saved := stopTimeout
	stopTimeout = 300 * time.Millisecond
	t.Cleanup(func() { stopTimeout = saved })

	// The child writes its pid, so a Stop that never kills it cannot leave
	// it running after the test.
	pidFile := filepath.Join(t.TempDir(), "pid")
	s := startStub(t, "echo $$ > "+pidFile+"\ntrap '' TERM")
	t.Cleanup(func() {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	done := make(chan error, 1)
	go func() { done <- s.Stop() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return 10 s after a 300 ms timeout: the child that ignores SIGTERM was never killed")
	}
	if exited, state := s.process.Exited(); !exited || state.Success() {
		t.Fatal("child should have been killed")
	}
}

// Rules: [RT-3].
func TestStopWithoutStart(t *testing.T) {
	if err := NewV2Ray().Stop(); err == nil {
		t.Fatal("Stop before Start must fail")
	}
}

// TestInitRendersEgressPolicy: a client cannot reach the blocked networks,
// loopback by name (the control API first of all), or port 25 unless the
// operator allows it; the control API is on the configured port.
//
// Rules: [EG-1], [EG-4], [EG-5].
func TestInitRendersEgressPolicy(t *testing.T) {
	stub(t, "trap 'exit 0' TERM")
	t.Cleanup(func() { common.SetEgress(common.Egress{}) })

	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	cfg := v2raytypes.NewConfig().WithDefaultValues()
	cfg.VMess.ListenPort = 8443
	cfg.API.Port = 23456
	if err := cfg.SaveToPath(filepath.Join(dir, v2raytypes.ConfigFileName)); err != nil {
		t.Fatal(err)
	}

	render := func() map[string]interface{} {
		t.Helper()

		s := NewV2Ray()
		if err := s.Init(dir); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "v2ray_config.json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]interface{}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("rendered config is not JSON: %v\n%s", err, raw)
		}
		return doc
	}

	doc := render()
	api := doc["inbounds"].([]interface{})[0].(map[string]interface{})
	if api["tag"] != "api" || api["listen"] != "127.0.0.1" || api["port"].(float64) != 23456 {
		t.Fatalf("api inbound: %v", api)
	}
	outbounds := doc["outbounds"].([]interface{})
	if outbounds[0].(map[string]interface{})["protocol"] != "freedom" ||
		outbounds[1].(map[string]interface{})["protocol"] != "blackhole" ||
		outbounds[1].(map[string]interface{})["tag"] != "blocked" {
		t.Fatalf("outbounds: %v", outbounds)
	}

	routing := doc["routing"].(map[string]interface{})
	if routing["domainStrategy"] != "IPIfNonMatch" {
		t.Fatalf("domainStrategy %v: a name that resolves to loopback would pass", routing["domainStrategy"])
	}
	rules := routing["rules"].([]interface{})
	if len(rules) != 4 {
		t.Fatalf("rules: %v", rules)
	}
	for _, r := range rules[1:] {
		if r.(map[string]interface{})["outboundTag"] != "blocked" {
			t.Fatalf("rule %v does not block", r)
		}
	}
	if d := rules[1].(map[string]interface{})["domain"].([]interface{}); len(d) != 1 || d[0] != "domain:localhost" {
		t.Fatalf("domain rule: %v", d)
	}
	if p := rules[2].(map[string]interface{}); p["port"] != "25" || p["network"] != "tcp" {
		t.Fatalf("port 25 rule: %v", p)
	}
	var ips []string
	for _, ip := range rules[3].(map[string]interface{})["ip"].([]interface{}) {
		ips = append(ips, ip.(string))
	}
	if strings.Join(ips, " ") != strings.Join(common.BlockedNetworks(), " ") {
		t.Fatalf("blocked networks: %v", ips)
	}

	common.SetEgress(common.Egress{AllowSMTP: true})
	if rules = render()["routing"].(map[string]interface{})["rules"].([]interface{}); len(rules) != 3 {
		t.Fatalf("allow_smtp: rules %v", rules)
	}
}

// TestInitRendersOnlyVMess: with TLS on or off, the inbound clients reach is
// VMess, which has a cipher of its own. VLESS has none, so a VLESS inbound
// without TLS would carry the proxy hop in cleartext.
//
// Rules: [PV-9].
func TestInitRendersOnlyVMess(t *testing.T) {
	stub(t, "trap 'exit 0' TERM")

	for _, tls := range []bool{false, true} {
		dir := t.TempDir()
		t.Setenv("TMPDIR", dir)
		certPEM, keyPEM, err := common.SelfSignedCertificate("dvpnd", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tls.crt"), certPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tls.key"), keyPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := v2raytypes.NewConfig().WithDefaultValues()
		cfg.VMess.TLS = tls
		if err := cfg.SaveToPath(filepath.Join(dir, v2raytypes.ConfigFileName)); err != nil {
			t.Fatal(err)
		}

		if err := NewV2Ray().Init(dir); err != nil {
			t.Fatalf("tls=%v: %v", tls, err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "v2ray_config.json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Inbounds []struct {
				Tag      string `json:"tag"`
				Protocol string `json:"protocol"`
			} `json:"inbounds"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("rendered config is not JSON: %v\n%s", err, raw)
		}
		served := 0
		for _, in := range doc.Inbounds {
			if in.Tag == "api" {
				continue
			}
			served++
			if in.Protocol != "vmess" {
				t.Errorf("tls=%v: inbound %q serves %q, want vmess", tls, in.Tag, in.Protocol)
			}
		}
		if served == 0 {
			t.Errorf("tls=%v: no inbound for clients", tls)
		}
	}
}

// TestConfigWithoutAPISection: a v2ray.toml written before [api] existed
// still starts, on a random API port.
//
// Rules: [RT-9].
func TestConfigWithoutAPISection(t *testing.T) {
	path := filepath.Join(t.TempDir(), v2raytypes.ConfigFileName)
	old := "[vmess]\nlisten_port = 8443\ntls = false\ntransport = \"tcp\"\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	v := viper.New()
	v.SetConfigFile(path)
	cfg, err := v2raytypes.ReadInConfig(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.API.Port == 0 || cfg.VMess.ListenPort != 8443 {
		t.Fatalf("api port %d, listen port %d", cfg.API.Port, cfg.VMess.ListenPort)
	}
	if !strings.Contains(cfg.String(), "[api]\n") {
		t.Fatalf("the file written back lacks [api]:\n%s", cfg)
	}

	cfg.API.Port = cfg.VMess.ListenPort
	if err := cfg.Validate(); err == nil {
		t.Fatal("the API port may not be the VMess port")
	}
}
