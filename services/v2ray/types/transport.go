// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package types

// Transport is the VMess inbound's transport. Only TCP is served: it is the
// one client apps are confirmed to use.
type Transport byte

// TransportTCP is TCP's byte in Info(), which the legacy endpoint returns.
const TransportTCP Transport = 0x01

func (t Transport) Byte() byte {
	return byte(t)
}

func (t Transport) IsValid() bool {
	return t == TransportTCP
}

func (t Transport) String() string {
	if t == TransportTCP {
		return "tcp"
	}

	return ""
}

func NewTransportFromString(v string) Transport {
	if v == "tcp" {
		return TransportTCP
	}

	return 0x00
}
