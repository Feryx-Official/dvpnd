// SPDX-License-Identifier: Apache-2.0

package wireguard

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"

	wgtypes "github.com/Feryx-Official/dvpnd/v9/services/wireguard/types"
)

// Rules: [CT-5].
func TestParsePeerRequest(t *testing.T) {
	key, err := wgtypes.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := key.Public()

	s := NewVariant(Default, nil)
	data, err := s.ParsePeerRequest([]byte(`{"public_key":"` + pub.String() + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 32 || base64.StdEncoding.EncodeToString(data) != pub.String() {
		t.Fatalf("key round trip failed: %x", data)
	}

	if _, err := s.ParsePeerRequest([]byte(`{"public_key":"not-base64!"}`)); err == nil {
		t.Error("bad key accepted")
	}
}

// Rules: [CT-3], [CT-4].
func TestHandshakePayload(t *testing.T) {
	s := NewVariant(Default, nil)
	binary.BigEndian.PutUint16(s.info, 51820)
	s.info[2] = 0xab

	v4 := []byte{10, 8, 0, 3}
	v6 := []byte{0xfd, 0x86, 0xea, 0x04, 0x11, 0x15, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3}
	zero := make([]byte, 16)

	payload, err := s.HandshakePayload(append(append([]byte{}, v4...), v6...))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(payload)
	want := `{"addrs":["10.8.0.3/32","fd86:ea04:1115::3/128"],"metadata":[{"port":51820,"public_key":"` + s.PublicKey() + `"}]}`
	if string(out) != want {
		t.Fatalf("dual-stack payload:\n got %s\nwant %s", out, want)
	}

	payload, err = s.HandshakePayload(append(append([]byte{}, v4...), zero...))
	if err != nil {
		t.Fatal(err)
	}
	if got := payload.(HandshakePayloadData).Addrs; len(got) != 1 || got[0] != "10.8.0.3/32" {
		t.Fatalf("ipv4-only peer must yield one address, got %v", got)
	}

	if _, err := s.HandshakePayload([]byte{1, 2, 3}); err == nil {
		t.Error("short result accepted")
	}

	if pub, _ := json.Marshal(s.PublicMetadata()); string(pub) != `[{"port":0,"public_key":null}]` {
		t.Fatalf("public metadata: %s", pub)
	}
	if s.Name() != "wireguard" {
		t.Fatalf("name: %s", s.Name())
	}
}

// The mirror of the proxy case: a client that speaks a proxy protocol reaches
// a tunnel node and sends a uuid. The refusal must name what this node speaks.
//
// Rules: [HS-11].
func TestParsePeerRequestExplainsTheProtocolMismatch(t *testing.T) {
	s := NewVariant(Default, nil)

	_, err := s.ParsePeerRequest([]byte(`{"uuid":"3f2504e0-4f89-41d3-9a0c-0305e82c3301"}`))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"this node runs wireguard", "public_key", "proxy client"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not mention %q", err, want)
		}
	}

	if _, err := s.ParsePeerRequest([]byte(`{}`)); err == nil ||
		!strings.Contains(err.Error(), "none was sent") {
		t.Fatalf("empty request: got %v", err)
	}
}
