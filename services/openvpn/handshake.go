// SPDX-License-Identifier: Apache-2.0

package openvpn

import (
	"encoding/binary"
	"errors"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
	ovpntypes "github.com/Feryx-Official/dvpnd/v9/services/openvpn/types"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// Name is the service_type string clients expect.
const Name = "openvpn"

// NewService builds the OpenVPN service for a node.
func NewService(_ *types.Config) (types.Service, error) {
	return NewOpenVPN(), nil
}

// Command is the "openvpn config …" CLI subtree.
func Command() *cobra.Command {
	return common.ConfigCommand(Name, []string{"ovpn"}, "OpenVPN sub-commands", common.ConfigSpec{
		FileName: ovpntypes.ConfigFileName,
		Default:  func() common.ConfigFile { return ovpntypes.NewConfig().WithDefaultValues() },
		Read:     func(v *viper.Viper) (common.ConfigFile, error) { return ovpntypes.ReadInConfig(v) },
	})
}

func (s *OpenVPN) Name() string {
	return Name
}

// ListenPort is the port encoded in the first two bytes of Info().
func (s *OpenVPN) ListenPort() uint16 {
	return binary.BigEndian.Uint16(s.info[:2])
}

// ParsePeerRequest reads {"uuid": …} (a 16-byte array, as client apps send
// it, or the canonical string) and returns the 16 raw bytes.
func (s *OpenVPN) ParsePeerRequest(raw []byte) ([]byte, error) {
	id, err := common.UUIDPeerRequest(s.Name(), raw)
	if err != nil {
		return nil, err
	}

	return id[:], nil
}

// PeerMetadata is the server's endpoint and the material shared by all
// clients. []byte fields marshal as standard base64, which is what client
// apps decode.
type PeerMetadata struct {
	Port     uint16 `json:"port"`
	Protocol string `json:"protocol"`
	CA       []byte `json:"ca"`
	TLS      []byte `json:"tls"`
}

// HandshakePayloadData is what an OpenVPN client receives in result.data:
// the metadata plus its own certificate and key. The node's hosts come from
// the envelope's addrs.
type HandshakePayloadData struct {
	Metadata []PeerMetadata `json:"metadata"`
	Cert     []byte         `json:"cert"`
	Key      []byte         `json:"key"`
}

// HandshakePayload unpacks AddPeer's result into the client's profile parts.
func (s *OpenVPN) HandshakePayload(result []byte) (interface{}, error) {
	if len(result) < 4 {
		return nil, errors.New("unexpected openvpn peer result")
	}
	certLen := int(binary.BigEndian.Uint32(result[:4]))
	if certLen == 0 || 4+certLen > len(result) {
		return nil, errors.New("unexpected openvpn peer result length")
	}
	if s.pki == nil {
		return nil, errors.New("openvpn was not initialised")
	}

	return HandshakePayloadData{
		Metadata: []PeerMetadata{{
			Port:     s.ListenPort(),
			Protocol: s.config.Proto,
			CA:       s.pki.caDER,
			TLS:      s.pki.tlsCrypt,
		}},
		Cert: result[4 : 4+certLen],
		Key:  result[4+certLen:],
	}, nil
}

// PublicInbound is the OpenVPN entry of the root document: the transport,
// with the port and the material handed out per session blank.
type PublicInbound struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	CA       []byte `json:"ca"`
	TLS      []byte `json:"tls"`
}

// PublicMetadata lists the listener with its transport only.
func (s *OpenVPN) PublicMetadata() interface{} {
	proto := ovpntypes.ProtoUDP
	if s.info[2] == transportTCP {
		proto = ovpntypes.ProtoTCP
	}

	return []PublicInbound{{Protocol: proto}}
}
