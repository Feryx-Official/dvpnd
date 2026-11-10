// SPDX-License-Identifier: Apache-2.0

package xray

import (
	"encoding/binary"
	"testing"

	xraytypes "github.com/Feryx-Official/dvpnd/v9/services/xray/types"
)

// TestInfoCarriesThePort: the legacy endpoint hands Info() to old clients,
// which read the listen port from its first two bytes, big-endian.
//
// Rules: [CT-6].
func TestInfoCarriesThePort(t *testing.T) {
	stubBinary(t)
	dir, cfg := home(t, xraytypes.SecurityTLS)
	s := NewXRay()
	if err := s.Init(dir); err != nil {
		t.Fatal(err)
	}
	if info := s.Info(); len(info) < 2 || binary.BigEndian.Uint16(info) != cfg.VLESS.ListenPort {
		t.Fatalf("Info() = %x, want the port %d first", info, cfg.VLESS.ListenPort)
	}
}
