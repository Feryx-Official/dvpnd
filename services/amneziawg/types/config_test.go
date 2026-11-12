// SPDX-License-Identifier: Apache-2.0

package types

import (
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestDefaultsValidateAndRoundTrip(t *testing.T) {
	c := NewConfig().WithDefaultValues()
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	o := c.Obfuscation
	if o.S1 < 15 || o.S1 > 150 || o.S2 < 15 || o.S2 > 150 || o.S3 != 0 || o.S4 != 0 {
		t.Fatalf("paddings: %+v", o)
	}
	if o.H1 <= 4 || o.H1 == o.H2 || o.H2 == o.H3 || o.H3 == o.H4 {
		t.Fatalf("headers: %+v", o)
	}

	v := c.V3
	if !v.Enabled || v.Interface != "awg1" || v.ListenPort == 0 || v.ListenPort == c.ListenPort || v.PrivateKey == c.PrivateKey {
		t.Fatalf("v3 identity: %+v", v)
	}
	if key, err := base64.StdEncoding.DecodeString(v.HeaderProtectionKey); err != nil || len(key) != 32 {
		t.Fatalf("v3 header protection key: %q", v.HeaderProtectionKey)
	}
	if v.S1 < 16 || v.S1 > 150 || v.S2 < 16 || v.S2 > 150 || v.S3 < 16 || v.S3 > 64 || v.S4 < 16 || v.S4 > 32 {
		t.Fatalf("v3 paddings: %+v", v)
	}
	if v.H1 <= 4 || v.H1 == v.H2 || v.H2 == v.H3 || v.H3 == v.H4 || !v.RandomTrailers || v.ContentPaddingAddition != "0-64" {
		t.Fatalf("v3 parameters: %+v", v)
	}

	path := filepath.Join(t.TempDir(), ConfigFileName)
	c.Obfuscation.I1 = "<b 0x1234><r 16>"
	if err := c.SaveToPath(path); err != nil {
		t.Fatal(err)
	}
	vp := viper.New()
	vp.SetConfigFile(path)
	read, err := ReadInConfig(vp)
	if err != nil {
		t.Fatal(err)
	}
	if read.String() != c.String() {
		t.Fatalf("round trip differs:\n%s\n---\n%s", read.String(), c.String())
	}
	if err := read.Validate(); err != nil {
		t.Fatal(err)
	}

	lines := read.Obfuscation.InterfaceLines()
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"Jc = 4", "Jmin = 40", "Jmax = 70", "S3 = 0", "I1 = <b 0x1234><r 16>"} {
		if !strings.Contains(joined, want) {
			t.Errorf("interface lines lack %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "I2 =") {
		t.Error("empty signature packets must be omitted")
	}
	if strings.Contains(joined, "MTU") || strings.Contains(joined, "HeaderProtectionKey") {
		t.Errorf("the default tier must not carry 3.1 keys:\n%s", joined)
	}

	joined = strings.Join(read.V3.InterfaceLines(read.Obfuscation), "\n")
	for _, want := range []string{
		"MTU = 1280", "Jc = 4", "Jmin = 40", "Jmax = 70",
		"S3 = " + itoa(v.S3), "H2 = " + itoa(v.H2),
		"HeaderProtectionKey = " + v.HeaderProtectionKey,
		"RandomTrailers = on", "ContentPaddingAddition = 0-64",
		"I1 = <b 0x1234><r 16>",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("v3 interface lines lack %q:\n%s", want, joined)
		}
	}

	if wg := read.WireGuard(); wg.Interface != "awg0" || wg.ListenPort != c.ListenPort || wg.PrivateKey != c.PrivateKey {
		t.Fatalf("wireguard part: %+v", wg)
	}
	if wg := read.WireGuardV3(); wg.Interface != "awg1" || wg.ListenPort != v.ListenPort || wg.PrivateKey != v.PrivateKey || wg.EnableIPv6 != c.EnableIPv6 {
		t.Fatalf("wireguard v3 part: %+v", wg)
	}
}

// Rules: [CT-7], [RT-9].
func TestReadInConfigWithoutV3Section(t *testing.T) {
	c := NewConfig().WithDefaultValues()
	older, _, _ := strings.Cut(c.String(), "[v3]")

	path := filepath.Join(t.TempDir(), ConfigFileName)
	if err := writeFile(path, older); err != nil {
		t.Fatal(err)
	}
	vp := viper.New()
	vp.SetConfigFile(path)
	read, err := ReadInConfig(vp)
	if err != nil {
		t.Fatal(err)
	}
	if read.V3Enabled() || read.V3.Interface != "awg1" {
		t.Fatalf("a file without [v3] must run the default tier only: %+v", read.V3)
	}
	if err := read.Validate(); err != nil {
		t.Fatalf("a file without [v3] must validate: %v", err)
	}
}

// Rules: [CT-7].
func TestObfuscationValidation(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Obfuscation)
		want string
	}{
		{"jc too big", func(o *Obfuscation) { o.Jc = 129 }, "jc"},
		{"jmin above jmax", func(o *Obfuscation) { o.Jmin, o.Jmax = 80, 70 }, "jmin"},
		{"s2 collides", func(o *Obfuscation) { o.S1, o.S2 = 20, 76 }, "s1 + 56"},
		{"s1 too big", func(o *Obfuscation) { o.S1 = 2000 }, "at most 1132"},
		{"header too small", func(o *Obfuscation) { o.H1 = 3 }, "above 4"},
		{"headers not distinct", func(o *Obfuscation) { o.H2 = o.H1 }, "distinct"},
		{"bad i packet", func(o *Obfuscation) { o.I3 = "junk" }, "i3"},
		{"tag the engines do not know", func(o *Obfuscation) { o.I2 = "<c>" }, "i2"},
	}
	for _, tc := range cases {
		o := (&Obfuscation{}).Generate()
		tc.edit(o)
		err := o.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want error containing %q", tc.name, err, tc.want)
		}
	}

	// All-zero headers are plain WireGuard framing and allowed.
	o := (&Obfuscation{}).Generate()
	o.H1, o.H2, o.H3, o.H4 = 0, 0, 0, 0
	if err := o.Validate(); err != nil {
		t.Fatalf("all-zero headers: %v", err)
	}
}

// Rules: [CT-7].
func TestV3Validation(t *testing.T) {
	cases := []struct {
		name string
		edit func(*V3)
		want string
	}{
		{"default interface", func(v *V3) { v.Interface = "awg0" }, "interface"},
		{"interface with shell syntax", func(v *V3) { v.Interface = "awg1;id" }, "interface"},
		{"default port", func(v *V3) { v.ListenPort = 51821 }, "listen_port"},
		{"bad private key", func(v *V3) { v.PrivateKey = "x" }, "private_key"},
		{"short header key", func(v *V3) { v.HeaderProtectionKey = "AAAA" }, "header_protection_key"},
		{"s3 too small for header protection", func(v *V3) { v.S3 = 11 }, "at least 12"},
		{"s2 collides", func(v *V3) { v.S1, v.S2 = 20, 76 }, "s1 + 56"},
		{"zero headers", func(v *V3) { v.H1, v.H2, v.H3, v.H4 = 0, 0, 0, 0 }, "above 4"},
		{"bad padding range", func(v *V3) { v.ContentPaddingAddition = "lots" }, "content_padding_addition"},
		{"inverted padding range", func(v *V3) { v.ContentPaddingAddition = "64-0" }, "content_padding_addition"},
		{"padding range too big", func(v *V3) { v.ContentPaddingAddition = "0-1000" }, "0-256"},
	}
	for _, tc := range cases {
		v := (&V3{}).Generate(51821)
		tc.edit(v)
		err := v.Validate("awg0", 51821)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want error containing %q", tc.name, err, tc.want)
		}
	}

	v := (&V3{}).Generate(51821)
	v.ContentPaddingAddition = "32"
	if err := v.Validate("awg0", 51821); err != nil {
		t.Fatalf("a single value is a range: %v", err)
	}
	v.Enabled = false
	c := NewConfig().WithDefaultValues()
	c.V3 = v
	if err := c.Validate(); err != nil {
		t.Fatalf("a disabled tier is not validated: %v", err)
	}
}

func TestParseRange(t *testing.T) {
	for _, tc := range []struct {
		in     string
		lo, hi uint32
		ok     bool
	}{
		{"0-64", 0, 64, true}, {"7", 7, 7, true}, {"", 0, 0, false}, {"1-2-3", 0, 0, false}, {"a-b", 0, 0, false},
	} {
		lo, hi, err := ParseRange(tc.in)
		if (err == nil) != tc.ok || lo != tc.lo || hi != tc.hi {
			t.Errorf("%q: %d %d %v", tc.in, lo, hi, err)
		}
	}
}
