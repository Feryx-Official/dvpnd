// SPDX-License-Identifier: Apache-2.0

package services

import (
	"testing"

	"github.com/Feryx-Official/dvpnd/v9/types"
)

// Every registry entry must agree with the service it constructs, and the
// numbers and names must be the ones clients on the network use.
//
// Rules: [CT-1].
func TestRegistryAgreesWithServices(t *testing.T) {
	want := map[string]uint64{"wireguard": 1, "v2ray": 2, "xray": 4, "hysteria2": 6, "amneziawg": 5, "openvpn": 3}

	for _, p := range All() {
		s, err := p.New(types.NewConfig().WithDefaultValues())
		if err != nil {
			t.Fatalf("%s: New: %v", p.Name, err)
		}
		if s.Name() != p.Name || s.Type() != p.Type {
			t.Errorf("%s: service reports %s/%d, registry says %s/%d", p.Name, s.Name(), s.Type(), p.Name, p.Type)
		}
		if n, ok := want[p.Name]; !ok || n != p.Type {
			t.Errorf("%s: type %d, want %d", p.Name, p.Type, n)
		}
		if p.Command().Use != p.Name {
			t.Errorf("%s: CLI subtree is %q", p.Name, p.Command().Use)
		}
		delete(want, p.Name)
	}
	for name := range want {
		t.Errorf("%s is not registered", name)
	}

	if _, err := Lookup("bogus"); err == nil {
		t.Error("unknown name accepted")
	}
}

// Rules: [CT-1].
func TestNodeTypesRegistered(t *testing.T) {
	cfg := types.NewConfig().WithDefaultValues()
	cfg.Node.Type = "bogus"
	if err := cfg.Node.Validate(); err == nil {
		t.Fatal("unknown node type validated")
	}

	cfg.Node.Type = "v2ray"
	cfg.Handshake.Enable = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("v2ray with handshake DNS validated")
	}
}

// TestHandshakeDNSTypesHaveATunnel: the Handshake resolver listens on the
// tunnel address, so every type allowed to run it must report one.
//
// Rules: [PV-8].
func TestHandshakeDNSTypesHaveATunnel(t *testing.T) {
	for _, p := range All() {
		if !p.HandshakeDNS {
			continue
		}
		s, err := p.New(types.NewConfig().WithDefaultValues())
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := s.(types.TunnelHost); !ok {
			t.Errorf("%s may run the Handshake resolver but has no tunnel address", p.Name)
		}
	}
}
