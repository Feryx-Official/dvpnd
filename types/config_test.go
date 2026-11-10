// SPDX-License-Identifier: Apache-2.0

package types

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/libs/geoip"
)

func TestBandwidthConfigValidate(t *testing.T) {
	if c := NewBandwidthConfig().WithDefaultValues(); c.DownloadMbps != 0 || c.UploadMbps != 0 {
		t.Fatalf("default must be zero (measure), got %+v", c)
	} else if err := c.Validate(); err != nil {
		t.Fatalf("default config must validate: %v", err)
	}

	cases := []struct {
		name string
		cfg  BandwidthConfig
		want string // substring of the error, empty when the config must be valid
	}{
		{"unset", BandwidthConfig{}, ""},
		{"declared", BandwidthConfig{DownloadMbps: 1000, UploadMbps: 1000}, ""},
		{"asymmetric", BandwidthConfig{DownloadMbps: 1000, UploadMbps: 50}, ""},
		{"fractional", BandwidthConfig{DownloadMbps: 0.5, UploadMbps: 0.5}, ""},
		{"at the maximum", BandwidthConfig{DownloadMbps: MaxBandwidthMbps, UploadMbps: MaxBandwidthMbps}, ""},

		{"download only", BandwidthConfig{DownloadMbps: 1000}, "set both"},
		{"upload only", BandwidthConfig{UploadMbps: 1000}, "set both"},
		{"negative download", BandwidthConfig{DownloadMbps: -1, UploadMbps: 10}, "download_mbps cannot be negative"},
		{"negative upload", BandwidthConfig{DownloadMbps: 10, UploadMbps: -1}, "upload_mbps cannot be negative"},
		{"too large", BandwidthConfig{DownloadMbps: MaxBandwidthMbps + 1, UploadMbps: 10}, "download_mbps cannot be greater than"},
		{"nan", BandwidthConfig{DownloadMbps: math.NaN(), UploadMbps: 10}, "download_mbps must be a number"},
		{"inf", BandwidthConfig{DownloadMbps: 10, UploadMbps: math.Inf(1)}, "upload_mbps must be a number"},
	}
	for _, c := range cases {
		err := c.cfg.Validate()
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: got %v, want error containing %q", c.name, err, c.want)
		}
	}
}

// The section survives a render and a parse: what `config set` writes as a
// bare number comes back as the float the code expects.
func TestBandwidthConfigRoundTrip(t *testing.T) {
	cfg := NewConfig().WithDefaultValues()
	cfg.Bandwidth.DownloadMbps, cfg.Bandwidth.UploadMbps = 1000, 250.5

	path := filepath.Join(t.TempDir(), ConfigFileName)
	if err := cfg.SaveToPath(path); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "download_mbps = 1000\n") {
		t.Fatalf("rendered file lacks the bare number:\n%s", data)
	}

	v := viper.New()
	v.SetConfigFile(path)
	back, err := ReadInConfig(v)
	if err != nil {
		t.Fatal(err)
	}
	if back.Bandwidth.DownloadMbps != 1000 || back.Bandwidth.UploadMbps != 250.5 {
		t.Fatalf("read back %+v", back.Bandwidth)
	}
	if err := back.Validate(); err != nil && !strings.Contains(err.Error(), "invalid section chain") &&
		!strings.Contains(err.Error(), "invalid section keyring") && !strings.Contains(err.Error(), "invalid section node") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

// Rules: [CT-13].
func TestGeoIPConfigValidate(t *testing.T) {
	if c := NewGeoIPConfig().WithDefaultValues(); c.Provider != geoip.ProviderAuto {
		t.Fatalf("default provider is %q, want auto", c.Provider)
	} else if err := c.Validate(); err != nil {
		t.Fatalf("default config must validate: %v", err)
	}

	cases := []struct {
		name string
		cfg  GeoIPConfig
		want string // substring of the error, empty when the config must be valid
	}{
		{"auto", GeoIPConfig{Provider: "auto"}, ""},
		{"ipwhois", GeoIPConfig{Provider: "ipwhois"}, ""},
		{"ip2location", GeoIPConfig{Provider: "ip2location"}, ""},
		{"ip2location with key", GeoIPConfig{Provider: "ip2location", Token: "k"}, ""},
		{"cloudflare", GeoIPConfig{Provider: "cloudflare"}, ""},
		{"ipify", GeoIPConfig{Provider: "ipify"}, ""},
		{"ip-api with paid url", GeoIPConfig{Provider: "ip-api", URL: "https://pro.ip-api.com/json/?key=k"}, ""},
		{"ipinfo with token", GeoIPConfig{Provider: "ipinfo", Token: "t"}, ""},
		{"none", GeoIPConfig{Provider: "none"}, ""},
		{"static values", GeoIPConfig{Provider: "auto", City: "Turin", Country: "IT", Latitude: 45.07, Longitude: 7.69}, ""},

		{"unknown", GeoIPConfig{Provider: "bogus"}, "provider must be one of"},
		{"empty", GeoIPConfig{}, "provider must be one of"},
		{"auto with url", GeoIPConfig{Provider: "auto", URL: "https://example.com"}, "url cannot be set"},
		{"none with url", GeoIPConfig{Provider: "none", URL: "https://example.com"}, "url cannot be set"},
		{"auto with token", GeoIPConfig{Provider: "auto", Token: "t"}, "token is only used"},
		{"ipify with token", GeoIPConfig{Provider: "ipify", Token: "t"}, "token is only used"},
		{"ipinfo without token", GeoIPConfig{Provider: "ipinfo"}, "token is required"},
		{"bad url", GeoIPConfig{Provider: "ipwhois", URL: "not a url"}, "invalid url"},
		{"latitude", GeoIPConfig{Provider: "auto", Latitude: 91}, "latitude"},
		{"longitude", GeoIPConfig{Provider: "auto", Longitude: -181}, "longitude"},
	}
	for _, c := range cases {
		err := c.cfg.Validate()
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: got %v, want error containing %q", c.name, err, c.want)
		}
	}
}

func TestConfigTemplateRenders(t *testing.T) {
	out := NewConfig().WithDefaultValues().String()
	for _, want := range []string{`provider = "auto"`, "[geoip]", `city = ""`, "latitude = 0.000000",
		"[bandwidth]", "download_mbps = 0\n", "upload_mbps = 0\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config lacks %q", want)
		}
	}
}

// Rules: [PV-7].
func TestConfigRedacted(t *testing.T) {
	c := NewConfig().WithDefaultValues()
	c.GeoIP.Provider = "ipinfo"
	c.GeoIP.Token = "s3cr3t-geoip-token"

	if out := c.Redacted().String(); strings.Contains(out, "s3cr3t-geoip-token") || !strings.Contains(out, `token = "<redacted>"`) {
		t.Fatalf("the logged configuration must not carry the token:\n%s", out)
	}
	if c.GeoIP.Token != "s3cr3t-geoip-token" {
		t.Fatal("Redacted must not change the configuration it copies")
	}

	c.GeoIP.Token = ""
	if out := c.Redacted().String(); !strings.Contains(out, `token = ""`) {
		t.Fatalf("an empty token stays empty:\n%s", out)
	}
}

// Rules: [PV-8].
func TestHandshakeResolverOffByDefault(t *testing.T) {
	if NewConfig().WithDefaultValues().Handshake.Enable {
		t.Fatal("the Handshake resolver needs hnsd, which is not installed with the node; it must be off by default")
	}
}

// TestEgressSMTPBlockedByDefault: a config.toml without [egress], as every
// node had before the section existed, keeps port 25 blocked; the operator's
// opt-in survives a round trip.
//
// Rules: [EG-4], [RT-9].
func TestEgressSMTPBlockedByDefault(t *testing.T) {
	if NewConfig().WithDefaultValues().Egress.AllowSMTP {
		t.Fatal("SMTP must be blocked by default")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(path, []byte("[node]\nmoniker = \"test node\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := viper.New()
	v.SetConfigFile(path)
	cfg, err := ReadInConfig(v)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Egress == nil || cfg.Egress.AllowSMTP {
		t.Fatalf("egress read from a file without the section: %+v", cfg.Egress)
	}

	cfg.Egress.AllowSMTP = true
	if err := cfg.SaveToPath(path); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "[egress]\n") ||
		!strings.Contains(string(data), "allow_smtp = true\n") {
		t.Fatalf("rendered file:\n%s", data)
	}
	v = viper.New()
	v.SetConfigFile(path)
	if back, err := ReadInConfig(v); err != nil || !back.Egress.AllowSMTP {
		t.Fatalf("read back %+v, %v", back.Egress, err)
	}
}

// TestRPCAddressesNeedTLS: the node trusts what its RPC answers, so plain
// http is accepted only for an RPC on the same host.
//
// Rules: [CH-1].
func TestRPCAddressesNeedTLS(t *testing.T) {
	for addr, ok := range map[string]bool{
		"https://rpc.example.com:443":                 true,
		"http://127.0.0.1:26657":                      true,
		"http://localhost:26657":                      true,
		"http://[::1]:26657":                          true,
		"http://rpc.example.com:80":                   false,
		"http://192.168.1.5:26657":                    false,
		"https://a.example:443,http://10.0.0.2:26657": false,
	} {
		c := NewChainConfig().WithDefaultValues()
		c.RPCAddresses = addr
		if err := c.Validate(); (err == nil) != ok {
			t.Errorf("%s: err = %v", addr, err)
		}
	}
}

// Rules: [CH-3].
func TestKeyringGranter(t *testing.T) {
	c := NewKeyringConfig().WithDefaultValues()
	if c.Granter != "" || c.Validate() != nil {
		t.Fatal("no granter by default")
	}
	c.Granter = "not-an-address"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "invalid granter") {
		t.Fatalf("granter validation: %v", err)
	}
}
