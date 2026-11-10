// SPDX-License-Identifier: Apache-2.0

package openvpn

import (
	"encoding/binary"
	"testing"

	ovpntypes "github.com/Feryx-Official/dvpnd/v9/services/openvpn/types"
)

// TestInfoCarriesThePort: the legacy endpoint hands Info() to old clients,
// which read the listen port from its first two bytes, big-endian.
//
// Rules: [CT-6].
func TestInfoCarriesThePort(t *testing.T) {
	stubBinary(t)
	dir, cfg := home(t, ovpntypes.ProtoUDP, false)
	s := NewOpenVPN()
	if err := s.Init(dir); err != nil {
		t.Fatal(err)
	}
	if info := s.Info(); len(info) < 2 || binary.BigEndian.Uint16(info) != cfg.ListenPort {
		t.Fatalf("Info() = %x, want the port %d first", info, cfg.ListenPort)
	}
}
