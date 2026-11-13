// SPDX-License-Identifier: Apache-2.0

// Package xray runs an Xray-core VLESS inbound as the node's VPN service. The
// proxy is a child process driven over its gRPC API on loopback; peers are
// VLESS users identified by a UUID. See docs/protocols.md.
package xray

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/spf13/viper"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
	xraytypes "github.com/Feryx-Official/dvpnd/v9/services/xray/types"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

const (
	// InfoLen: port (2), proxy (1), transport (1), security (1).
	InfoLen = 2 + 1 + 1 + 1

	// proxyVLESS is the proxy byte that leads the peer data; the only proxy
	// this service runs.
	proxyVLESS = 0x01

	flowVision = "xtls-rprx-vision"
)

var (
	_ types.Service = (*XRay)(nil)

	// binaryName is the proxy binary looked up on PATH; a variable so tests
	// can point it at a stub.
	binaryName = "xray"

	// stopTimeout is how long Stop waits after SIGTERM before killing.
	stopTimeout = 5 * time.Second

	// rpcTimeout bounds every call to the proxy's control API.
	rpcTimeout = 10 * time.Second
)

type XRay struct {
	info    []byte
	process *common.Process
	config  *xraytypes.Config
	peers   *common.PeerSet
	// tlsPin is the hex SHA-256 of the certificate on the TLS inbound;
	// empty with REALITY.
	tlsPin string
	// configPath is the rendered configuration, written by Init.
	configPath string

	connMu sync.Mutex // guards conn: handshakes and the jobs call the API at once
	conn   *grpc.ClientConn
}

func NewXRay() *XRay {
	return &XRay{
		info:   make([]byte, InfoLen),
		config: xraytypes.NewConfig(),
		peers:  common.NewPeerSet(),
	}
}

func (s *XRay) Type() uint64 {
	return xraytypes.Type
}

func (s *XRay) Info() []byte {
	return s.info
}

// configFileName is the rendered configuration in the runtime directory.
const configFileName = "xray_config.json"

func (s *XRay) Init(home string) (err error) {
	if _, err = exec.LookPath(binaryName); err != nil {
		return fmt.Errorf("the %q binary is not on PATH: install it on the host "+
			"(docs/operator.md, section 6) or run the Docker image, which bundles it: %w",
			binaryName, err)
	}

	v := viper.New()
	v.SetConfigFile(filepath.Join(home, xraytypes.ConfigFileName))

	s.config, err = xraytypes.ReadInConfig(v)
	if err != nil {
		return err
	}
	if err = s.config.Validate(); err != nil {
		return err
	}

	data := templateData{
		VLESS:       s.config.VLESS,
		Reality:     s.config.Reality,
		API:         s.config.API,
		TLSCertPath: filepath.Join(home, "tls.crt"),
		TLSKeyPath:  filepath.Join(home, "tls.key"),
		LogLevel:    logLevel(),

		BlockedNetworks: common.BlockedNetworks(),
		BlockedDomain:   common.BlockedLocalDomain,
		SMTPPort:        common.SMTPPort,
		AllowSMTP:       common.EgressPolicy().AllowSMTP,
	}

	security := types.TransportSecurityReality
	if s.config.VLESS.Security == xraytypes.SecurityTLS {
		security = types.TransportSecurityTLS
		s.tlsPin, err = common.CertificatePin(data.TLSCertPath)
		if err != nil {
			return err
		}
		if _, err = os.Stat(data.TLSKeyPath); err != nil {
			return fmt.Errorf("tls key: %w", err)
		}
		// xray may run as the proxy account, which cannot read the home.
		data.TLSCertPath, data.TLSKeyPath, err = common.CurrentRuntime().TLSFiles(home)
		if err != nil {
			return err
		}
	}

	t, err := template.New("xray_json").Funcs(template.FuncMap{"json": common.JSON}).Parse(configTemplate)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	if err = t.Execute(&buf, data); err != nil {
		return err
	}
	if s.configPath, err = common.CurrentRuntime().WriteFile(configFileName, buf.Bytes()); err != nil {
		return err
	}

	binary.BigEndian.PutUint16(s.info[0:], s.config.VLESS.ListenPort)
	s.info[2] = proxyVLESS
	s.info[3] = types.TransportProtocolTCP
	s.info[4] = byte(security)

	return nil
}

func (s *XRay) Start() (err error) {
	s.process, err = common.StartProxy(binaryName, []string{"run", "-config", s.configPath}, nil, s.config.VLESS.ListenPort)

	return err
}

// Done is closed once the proxy has exited.
func (s *XRay) Done() <-chan struct{} {
	return s.process.Done()
}

// Stop asks the proxy to exit and waits for it, killing it after stopTimeout.
func (s *XRay) Stop() error {
	s.connMu.Lock()
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
	s.connMu.Unlock()

	return s.process.Stop(stopTimeout)
}

// clientConn returns the connection to the proxy's control API, opened on
// first use. grpc.NewClient does not connect until a call is made, so a call's
// context bounds the wait for the proxy to come up.
func (s *XRay) clientConn() (*grpc.ClientConn, error) {
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

// peerKey is the session key: base64 of the peer data. It is also the
// user's email in xray, which is how statistics are keyed.
func peerKey(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func checkPeerData(data []byte) error {
	if len(data) != 1+common.UUIDLen || data[0] != proxyVLESS {
		return fmt.Errorf("peer data must be the VLESS byte followed by a %d-byte uuid", common.UUIDLen)
	}

	return nil
}

func (s *XRay) AddPeer(data []byte) ([]byte, error) {
	if err := checkPeerData(data); err != nil {
		return nil, err
	}

	conn, err := s.clientConn()
	if err != nil {
		return nil, err
	}

	var id [common.UUIDLen]byte
	copy(id[:], data[1:])

	flow := ""
	if s.config.VLESS.Flow {
		flow = flowVision
	}

	key := peerKey(data)
	ctx, cancel := rpcContext()
	defer cancel()

	// xray keeps a removed user's counters, so a later session with the
	// client's UUID would start with what an earlier one moved: they are
	// reset first, since the node bills on them from this moment.
	if _, err = invoke(ctx, conn, methodQueryStats, queryStatsRequest("user>>>"+key+">>>traffic>>>", true)); err != nil {
		return nil, err
	}
	if _, err = invoke(ctx, conn, methodAlterInbound, addUserRequest(xraytypes.InboundTag, key, common.FormatUUID(id), flow)); err != nil {
		return nil, err
	}
	s.peers.Put(key)

	return nil, nil
}

func (s *XRay) HasPeer(data []byte) bool {
	return s.peers.Has(peerKey(data))
}

func (s *XRay) RemovePeer(data []byte) error {
	if err := checkPeerData(data); err != nil {
		return err
	}

	conn, err := s.clientConn()
	if err != nil {
		return err
	}

	key := peerKey(data)
	ctx, cancel := rpcContext()
	defer cancel()

	if _, err = invoke(ctx, conn, methodAlterInbound, removeUserRequest(xraytypes.InboundTag, key)); err != nil {
		if !strings.Contains(err.Error(), "not found") {
			return err
		}
	}
	s.peers.Delete(key)

	return nil
}

// Peers reads every user's traffic counters in one query and reports the
// registered peers; a peer without counters yet reports zero.
func (s *XRay) Peers() ([]types.Peer, error) {
	conn, err := s.clientConn()
	if err != nil {
		return nil, err
	}

	ctx, cancel := rpcContext()
	defer cancel()

	res, err := invoke(ctx, conn, methodQueryStats, queryStatsRequest("user>>>", false))
	if err != nil {
		return nil, err
	}

	counters, err := parseQueryStatsResponse(res)
	if err != nil {
		return nil, err
	}

	keys := s.peers.Keys()
	items := make([]types.Peer, 0, len(keys))
	for _, key := range keys {
		items = append(items, types.Peer{
			Key:      key,
			Upload:   counters["user>>>"+key+">>>traffic>>>uplink"],
			Download: counters["user>>>"+key+">>>traffic>>>downlink"],
		})
	}

	return items, nil
}

func (s *XRay) PeerCount() int {
	return s.peers.Len()
}
