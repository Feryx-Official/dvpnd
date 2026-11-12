// SPDX-License-Identifier: Apache-2.0

package xray

import (
	"encoding/binary"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
	xraytypes "github.com/Feryx-Official/dvpnd/v9/services/xray/types"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// Name is the service_type string clients expect.
const Name = "xray"

// NewService builds the XRAY service for a node.
func NewService(_ *types.Config) (types.Service, error) {
	return NewXRay(), nil
}

// Command is the "xray config …" CLI subtree.
func Command() *cobra.Command {
	return common.ConfigCommand(Name, nil, "XRAY sub-commands", common.ConfigSpec{
		FileName: xraytypes.ConfigFileName,
		Default:  func() common.ConfigFile { return xraytypes.NewConfig().WithDefaultValues() },
		Read:     func(v *viper.Viper) (common.ConfigFile, error) { return xraytypes.ReadInConfig(v) },
		Check:    checkSet,
	})
}

// checkSet refuses at "config set" the [vless] values the node would refuse
// at start, a cleartext security among them. The [reality] section is left
// to start: a key pair set one key at a time is mismatched in between.
func checkSet(c common.ConfigFile) error {
	if err := c.(*xraytypes.Config).VLESS.Validate(); err != nil {
		return fmt.Errorf("invalid section vless: %w", err)
	}

	return nil
}

func (s *XRay) Name() string {
	return Name
}

// ListenPort is the port encoded in the first two bytes of Info().
func (s *XRay) ListenPort() uint16 {
	return binary.BigEndian.Uint16(s.info[:2])
}

// ParsePeerRequest reads {"uuid": …} (a 16-byte array or the canonical
// string) and returns the VLESS byte followed by the 16 UUID bytes.
func (s *XRay) ParsePeerRequest(raw []byte) ([]byte, error) {
	id, err := common.UUIDPeerRequest(s.Name(), raw)
	if err != nil {
		return nil, err
	}

	return append([]byte{proxyVLESS}, id[:]...), nil
}

// HandshakePayloadData is what an XRAY client receives in result.data.
type HandshakePayloadData struct {
	Metadata []types.Inbound `json:"metadata"`
}

// HandshakePayload describes the VLESS inbound with everything the client
// needs to build its outbound: the certificate pin for TLS, or the REALITY
// public key, server name, short id and fingerprint.
func (s *XRay) HandshakePayload(_ []byte) (interface{}, error) {
	return HandshakePayloadData{Metadata: []types.Inbound{s.inbound()}}, nil
}

// inbound describes the VLESS inbound with the pin or the REALITY
// parameters and the flow: what a client builds its outbound from.
func (s *XRay) inbound() types.Inbound {
	entry := types.Inbound{
		Port:              s.ListenPort(),
		ProxyProtocol:     types.ProxyProtocolVLESS,
		TransportProtocol: types.TransportProtocolTCP,
		TransportSecurity: int(s.info[4]),
		Flow:              types.FlowNone,
	}
	if s.config.VLESS.Flow {
		entry.Flow = types.FlowVision
	}

	switch entry.TransportSecurity {
	case types.TransportSecurityTLS:
		entry.TLSPin = s.tlsPin
	case types.TransportSecurityReality:
		entry.RealityServerName = s.config.Reality.ServerName
		entry.RealityShortID = s.config.Reality.ShortID
		entry.RealityPublicKey = s.config.Reality.PublicKey
		entry.RealityFingerprint = s.config.Reality.Fingerprint
	}

	return entry
}

// PublicInbound is the XRAY entry of the root document: the codes and the
// flow, with every per-session or secret value blank, in the layout nodes
// on the network publish.
type PublicInbound struct {
	Port               string `json:"port"`
	ProxyProtocol      int    `json:"proxy_protocol"`
	TransportProtocol  int    `json:"transport_protocol"`
	TransportSecurity  int    `json:"transport_security"`
	Flow               int    `json:"flow"`
	Method             string `json:"method"`
	Key                string `json:"key"`
	TLSPin             string `json:"tls_pin"`
	RealityServerName  string `json:"reality_server_name"`
	RealityShortID     string `json:"reality_short_id"`
	RealityPublicKey   string `json:"reality_public_key"`
	RealityFingerprint string `json:"reality_fingerprint"`
}

// PublicMetadata lists the VLESS inbound with its codes and flow only.
func (s *XRay) PublicMetadata() interface{} {
	in := s.inbound()

	return []PublicInbound{{
		ProxyProtocol:     in.ProxyProtocol,
		TransportProtocol: in.TransportProtocol,
		TransportSecurity: in.TransportSecurity,
		Flow:              in.Flow,
	}}
}
