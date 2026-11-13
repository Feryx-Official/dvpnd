// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"net"
	"strings"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// TestHnsdArgs: the resolver listens on the tunnel address only, never on
// every address, and keeps no log.
//
// Rules: [PV-8].
func TestHnsdArgs(t *testing.T) {
	got := strings.Join(hnsdArgs(8, net.IPv4(10, 8, 0, 1)), " ")
	if got != "--log-file /dev/null --pool-size 8 --rs-host 10.8.0.1:53" {
		t.Fatalf("hnsd args: %s", got)
	}
}

// TestAPIPort: the port tunnel clients may still reach on the node, taken
// from the API's listen address.
//
// Rules: [EG-8].
func TestAPIPort(t *testing.T) {
	for in, want := range map[string]uint16{
		"0.0.0.0:8585": 8585,
		"[::]:443":     443,
		":7777":        7777,
		"0.0.0.0":      0,
		"0.0.0.0:http": 0,
		"":             0,
	} {
		if got := apiPort(in); got != want {
			t.Errorf("apiPort(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestFitInterval: the chain deactivates a node, and ends a session, whose
// last update is older than its status_timeout; whatever the operator set,
// the node updates at least every 80% of it.
//
// Rules: [SL-13].
func TestFitInterval(t *testing.T) {
	for _, c := range []struct {
		configured, timeout, want time.Duration
		lower                     bool
	}{
		{2 * time.Hour, time.Hour, 48 * time.Minute, true},
		{48 * time.Minute, time.Hour, 48 * time.Minute, false},
		{30 * time.Minute, time.Hour, 30 * time.Minute, false},
		{2 * time.Hour, 0, 2 * time.Hour, false}, // no timeout known: as configured
	} {
		got, lower := fitInterval(c.configured, c.timeout)
		if got != c.want || lower != c.lower {
			t.Errorf("configured %s, status_timeout %s: got %s (lowered %v), want %s (%v)",
				c.configured, c.timeout, got, lower, c.want, c.lower)
		}
	}
}

// TestGranterOf: no granter is a node that signs with its own account; a
// granter that is the signing key itself, or not an address, is refused.
//
// Rules: [CH-3].
func TestGranterOf(t *testing.T) {
	signer := sdk.AccAddress(bytes.Repeat([]byte{1}, 20))
	other := sdk.AccAddress(bytes.Repeat([]byte{2}, 20))

	if g, err := granterOf("", signer); g != nil || err != nil {
		t.Errorf("empty: %v, %v; want no granter and no error", g, err)
	}
	if g, err := granterOf(other.String(), signer); err != nil || !g.Equals(other) {
		t.Errorf("another account: %v, %v", g, err)
	}
	if _, err := granterOf(signer.String(), signer); err == nil {
		t.Error("the signing key's own address was accepted as its granter")
	}
	if _, err := granterOf("not-an-address", signer); err == nil {
		t.Error("a value that is not an address was accepted")
	}
}

// TestUnencryptedKeyWarning: a node that signs with its own account from the
// test keyring, which stores keys without a passphrase, says so; with a hot
// key the account and its earnings stay off the host, and the other backends
// encrypt the key.
//
// Rules: [PV-13].
func TestUnencryptedKeyWarning(t *testing.T) {
	granter := sdk.AccAddress(bytes.Repeat([]byte{2}, 20))
	if w := unencryptedKeyWarning("test", nil); !strings.Contains(w, "unencrypted") || !strings.Contains(w, "[keyring] granter") {
		t.Errorf("own account in the test keyring: %q; want a warning naming the hot key setting", w)
	}
	for _, c := range []struct {
		backend string
		granter sdk.AccAddress
	}{{"test", granter}, {"file", nil}, {"os", nil}, {"file", granter}} {
		if w := unencryptedKeyWarning(c.backend, c.granter); w != "" {
			t.Errorf("backend %s, granter %v: %q; want no warning", c.backend, c.granter, w)
		}
	}
}
