// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package v2ray

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/viper"
	proxymancommand "github.com/v2fly/v2ray-core/v5/app/proxyman/command"
	statscommand "github.com/v2fly/v2ray-core/v5/app/stats/command"
	"github.com/v2fly/v2ray-core/v5/common/protocol"
	"github.com/v2fly/v2ray-core/v5/common/serial"
	"github.com/v2fly/v2ray-core/v5/common/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
	v2raytypes "github.com/Feryx-Official/dvpnd/v9/services/v2ray/types"
	"github.com/Feryx-Official/dvpnd/v9/types"
	"github.com/Feryx-Official/dvpnd/v9/utils"
)

const (
	InfoLen = 2 + 1 + 1
)

var (
	_ types.Service = (*V2Ray)(nil)
)

type V2Ray struct {
	info    []byte
	process *common.Process
	config  *v2raytypes.Config
	peers   *v2raytypes.Peers
	tlsPin  string
	// configPath is the rendered configuration, written by Init.
	configPath string

	connMu sync.Mutex // guards conn: handshakes and the jobs call the API at once
	conn   *grpc.ClientConn
}

// binaryName is the proxy binary looked up on PATH; a variable so tests can
// point it at a stub.
var binaryName = "v2ray"

// stopTimeout is how long Stop waits for the child to exit after SIGTERM
// before killing it.
var stopTimeout = 5 * time.Second

// rpcTimeout bounds every call to the proxy's control API, so a proxy that
// does not answer cannot hold a handshake or a job forever.
var rpcTimeout = 10 * time.Second

func NewV2Ray() *V2Ray {
	return &V2Ray{
		info:   make([]byte, InfoLen),
		config: v2raytypes.NewConfig(),
		peers:  v2raytypes.NewPeers(),
	}
}

// configFileName is the rendered configuration in the runtime directory.
const configFileName = "v2ray_config.json"

func (s *V2Ray) Type() uint64 {
	return v2raytypes.Type
}

func (s *V2Ray) Info() []byte {
	return s.info
}

// TLSPin is the hex SHA-256 of the certificate presented on the TLS inbound,
// which clients pin because the node's certificate is self-signed. Empty when
// TLS is disabled.
func (s *V2Ray) TLSPin() string {
	return s.tlsPin
}

func (s *V2Ray) Init(home string) (err error) {
	if _, err = exec.LookPath(binaryName); err != nil {
		return fmt.Errorf("the %q binary is not on PATH: install it on the host "+
			"(docs/operator.md, section 6) or run the Docker image, which bundles it: %w",
			binaryName, err)
	}

	v := viper.New()
	v.SetConfigFile(filepath.Join(home, v2raytypes.ConfigFileName))

	s.config, err = v2raytypes.ReadInConfig(v)
	if err != nil {
		return err
	}
	if err = s.config.Validate(); err != nil {
		return err
	}

	if s.config.VMess.TLS {
		s.config.VMess.Security = "tls"
	}
	if s.config.VMess.TLS {
		s.tlsPin, err = common.CertificatePin(filepath.Join(home, "tls.crt"))
		if err != nil {
			return err
		}
		// v2ray may run as the proxy account, which cannot read the home.
		s.config.VMess.TLSCertPath, s.config.VMess.TLSKeyPath, err = common.CurrentRuntime().TLSFiles(home)
		if err != nil {
			return err
		}
	}

	t, err := template.New("v2ray_json").Funcs(template.FuncMap{"json": common.JSON}).Parse(configTemplate)
	if err != nil {
		return err
	}

	data := templateData{
		Config:          s.config,
		BlockedNetworks: common.BlockedNetworks(),
		BlockedDomain:   common.BlockedLocalDomain,
		SMTPPort:        common.SMTPPort,
		AllowSMTP:       common.EgressPolicy().AllowSMTP,
	}

	var buf bytes.Buffer
	if err = t.Execute(&buf, data); err != nil {
		return err
	}
	if s.configPath, err = common.CurrentRuntime().WriteFile(configFileName, buf.Bytes()); err != nil {
		return err
	}

	binary.BigEndian.PutUint16(s.info[0:], s.config.VMess.ListenPort)
	transport := v2raytypes.NewTransportFromString(s.config.VMess.Transport)
	s.info[2] = transport.Byte()
	s.info[3] = utils.ByteFromBool(s.config.VMess.TLS)

	return nil
}

func (s *V2Ray) Start() (err error) {
	// VMess runs with v2ray's default, AEAD headers only: the legacy
	// header's MD5 authentication is weak, and current clients (alterId 0
	// on a v2ray or xray core) send AEAD.
	s.process, err = common.StartProxy(binaryName,
		[]string{"run", "--config", s.configPath}, nil, s.config.VMess.ListenPort)

	return err
}

// Done is closed once the proxy has exited.
func (s *V2Ray) Done() <-chan struct{} {
	return s.process.Done()
}

// Stop asks the proxy to exit and waits for it, killing it after stopTimeout.
func (s *V2Ray) Stop() error {
	s.connMu.Lock()
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
	s.connMu.Unlock()

	return s.process.Stop(stopTimeout)
}

// clientConn returns the connection to the proxy's control API, opened on
// first use. grpc.NewClient does not connect until a call is made, so a
// call's context bounds the wait for the proxy to come up.
func (s *V2Ray) clientConn() (*grpc.ClientConn, error) {
	s.connMu.Lock()
	defer s.connMu.Unlock()

	if s.conn != nil {
		return s.conn, nil
	}

	conn, err := grpc.NewClient(
		fmt.Sprintf("127.0.0.1:%d", s.config.API.Port),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// A call waits, up to its deadline, for the proxy to accept
		// connections instead of failing at once: right after Start, or
		// while the proxy restarts.
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
	)
	if err != nil {
		return nil, err
	}
	s.conn = conn

	return conn, nil
}

func rpcContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), rpcTimeout)
}

func (s *V2Ray) handlerServiceClient() (proxymancommand.HandlerServiceClient, error) {
	conn, err := s.clientConn()
	if err != nil {
		return nil, err
	}

	return proxymancommand.NewHandlerServiceClient(conn), nil
}

func (s *V2Ray) statsServiceClient() (statscommand.StatsServiceClient, error) {
	conn, err := s.clientConn()
	if err != nil {
		return nil, err
	}

	return statscommand.NewStatsServiceClient(conn), nil
}

func (s *V2Ray) AddPeer(data []byte) (result []byte, err error) {
	if len(data) != 1+16 {
		return nil, errors.New("data length must be 17 bytes")
	}

	client, err := s.handlerServiceClient()
	if err != nil {
		return nil, err
	}
	stats, err := s.statsServiceClient()
	if err != nil {
		return nil, err
	}

	var (
		email  = base64.StdEncoding.EncodeToString(data)
		proxy  = v2raytypes.Proxy(data[0])
		uid, _ = uuid.ParseBytes(data[1:])
	)

	req := &proxymancommand.AlterInboundRequest{
		Tag: proxy.Tag(),
		Operation: serial.ToTypedMessage(
			&proxymancommand.AddUserOperation{
				User: &protocol.User{
					Level:   0,
					Email:   email,
					Account: proxy.Account(uid),
				},
			},
		),
	}

	ctx, cancel := rpcContext()
	defer cancel()

	// v2ray keeps a removed user's counters, so a later session with the
	// client's UUID would start with what an earlier one moved: they are
	// reset first, since the node bills on them from this moment.
	_, err = stats.QueryStats(ctx, &statscommand.QueryStatsRequest{
		Pattern: fmt.Sprintf("user>>>%s>>>traffic>>>", email),
		Reset_:  true,
	})
	if err != nil {
		return nil, err
	}

	_, err = client.AlterInbound(ctx, req)
	if err != nil {
		return nil, err
	}

	s.peers.Put(
		v2raytypes.Peer{
			Email: email,
		},
	)

	return result, nil
}

func (s *V2Ray) HasPeer(data []byte) bool {
	var (
		email = base64.StdEncoding.EncodeToString(data)
		peer  = s.peers.Get(email)
	)

	return !peer.Empty()
}

func (s *V2Ray) RemovePeer(data []byte) error {
	if len(data) != 1+16 {
		return errors.New("data length must be 17 bytes")
	}

	client, err := s.handlerServiceClient()
	if err != nil {
		return err
	}

	var (
		email = base64.StdEncoding.EncodeToString(data)
		proxy = v2raytypes.Proxy(data[0])
	)

	req := &proxymancommand.AlterInboundRequest{
		Tag: proxy.Tag(),
		Operation: serial.ToTypedMessage(
			&proxymancommand.RemoveUserOperation{
				Email: email,
			},
		),
	}

	ctx, cancel := rpcContext()
	defer cancel()

	_, err = client.AlterInbound(ctx, req)
	if err != nil {
		if !strings.Contains(err.Error(), "not found") {
			return err
		}
	}

	s.peers.Delete(email)

	return nil
}

func (s *V2Ray) Peers() (items []types.Peer, err error) {
	client, err := s.statsServiceClient()
	if err != nil {
		return nil, err
	}

	err = s.peers.Iterate(
		func(key string, _ v2raytypes.Peer) (bool, error) {
			ctx, cancel := rpcContext()
			defer cancel()

			req := &statscommand.GetStatsRequest{
				Reset_: false,
				Name:   fmt.Sprintf("user>>>%s>>>traffic>>>uplink", key),
			}

			res, err := client.GetStats(ctx, req)
			if err != nil {
				if !strings.Contains(err.Error(), "not found") {
					return false, err
				}
			}

			upLink := res.GetStat()
			if upLink == nil {
				upLink = &statscommand.Stat{}
			}

			req = &statscommand.GetStatsRequest{
				Reset_: false,
				Name:   fmt.Sprintf("user>>>%s>>>traffic>>>downlink", key),
			}

			res, err = client.GetStats(ctx, req)
			if err != nil {
				if !strings.Contains(err.Error(), "not found") {
					return false, err
				}
			}

			downLink := res.GetStat()
			if downLink == nil {
				downLink = &statscommand.Stat{}
			}

			items = append(
				items,
				types.Peer{
					Key:      key,
					Upload:   upLink.GetValue(),
					Download: downLink.GetValue(),
				},
			)
			return false, nil
		},
	)

	if err != nil {
		return nil, err
	}

	return items, nil
}

func (s *V2Ray) PeerCount() int {
	return s.peers.Len()
}
