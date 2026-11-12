// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package types

// Service is one VPN protocol as the node drives it. The first group of
// methods manages the data plane; the second speaks the client handshake
// documented in docs/protocols.md. Info()'s first two bytes must be the
// listen port, big-endian.
type Service interface {
	Type() uint64
	Name() string
	Info() []byte
	Init(home string) error
	Start() error
	Stop() error
	AddPeer(data []byte) ([]byte, error)
	HasPeer(data []byte) bool
	RemovePeer(data []byte) error
	Peers() ([]Peer, error)
	PeerCount() int

	// ParsePeerRequest turns the client's JSON peer request into the bytes
	// AddPeer expects; base64 of those bytes is the session key.
	ParsePeerRequest(raw []byte) ([]byte, error)
	// HandshakePayload renders the JSON-encodable configuration returned to
	// the client in result.data, from AddPeer's result.
	HandshakePayload(result []byte) (interface{}, error)
	// PublicMetadata lists the service's inbounds for the node's root
	// document: the keys a handshake entry carries, with everything minted
	// per session or secret blanked (port 0 or "", keys null, pins ""), in
	// the layout nodes on the network publish for the type.
	PublicMetadata() interface{}
}

// Supervised is a service whose data plane is a child process the node
// started: a proxy or a VPN daemon. Done, called after Start, is closed once
// that process, or the node's channel to it, is gone. The node then serves
// nothing while it stays active on the chain, so it stops with an error and
// its service manager starts it again.
type Supervised interface {
	Done() <-chan struct{}
}

type Peer struct {
	Key      string `json:"key"`
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
}

// Inbound describes one listener of the VPN service in the shape client apps
// read from a node's root document and handshake response. The numeric codes
// are the ones clients compare against.
type Inbound struct {
	Port              uint16 `json:"port"`
	ProxyProtocol     int    `json:"proxy_protocol"`
	TransportProtocol int    `json:"transport_protocol"`
	TransportSecurity int    `json:"transport_security"`
	TLSPin            string `json:"tls_pin,omitempty"`

	// VLESS flow control and REALITY parameters (XRAY nodes), handshake only.
	Flow               int    `json:"flow,omitempty"`
	RealityServerName  string `json:"reality_server_name,omitempty"`
	RealityShortID     string `json:"reality_short_id,omitempty"`
	RealityPublicKey   string `json:"reality_public_key,omitempty"`
	RealityFingerprint string `json:"reality_fingerprint,omitempty"`
}

const (
	ProxyProtocolVLESS = 1
	ProxyProtocolVMess = 2

	TransportProtocolTCP = 1

	TransportSecurityNone    = 1
	TransportSecurityTLS     = 2
	TransportSecurityReality = 3

	FlowNone   = 1
	FlowVision = 2
)
