// SPDX-License-Identifier: Apache-2.0

// Package hysteria runs a Hysteria2 server as the node's VPN service. The
// server is a child process on one UDP port; it asks the node over HTTP on
// loopback whether a connecting client is a registered peer, and the node
// reads per-client traffic from the server's statistics API. Peers are
// identified by a UUID, which is also the password the client presents. See
// docs/protocols.md.
package hysteria

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"text/template"
	"time"

	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
	hysteriatypes "github.com/Feryx-Official/dvpnd/v9/services/hysteria/types"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

const (
	// InfoLen: port (2), obfuscation flag (1).
	InfoLen = 2 + 1

	// udpBufferBytes is what the kernel's socket buffer limits are raised to
	// for QUIC; hysteria warns below this.
	udpBufferBytes = 16 << 20
)

var (
	_ types.Service = (*Hysteria)(nil)

	// binaryName is the server binary looked up on PATH; a variable so tests
	// can point it at a stub.
	binaryName = "hysteria"

	// stopTimeout is how long Stop waits after SIGTERM before killing.
	stopTimeout = 5 * time.Second

	// apiTimeout bounds every call to the statistics API.
	apiTimeout = 10 * time.Second

	// udpBufferSwitches are the sysctls raised at start, best effort: on a
	// host the node can write them; in a container they are read-only and
	// must be set on the host.
	udpBufferSwitches = []string{"/proc/sys/net/core/rmem_max", "/proc/sys/net/core/wmem_max"}
)

// counters accumulates a peer's traffic across statistics reads, since the
// API is read with clear=1 and returns deltas.
type counters struct {
	upload   int64
	download int64
}

type Hysteria struct {
	info        []byte
	process     *common.Process
	config      *hysteriatypes.Config
	tlsPin      string
	statsSecret string
	configPath  string

	mu       sync.Mutex
	byAuth   map[string]string    // canonical uuid → session key
	traffic  map[string]*counters // session key → accumulated traffic
	peers    *common.PeerSet
	auth     *http.Server
	authDone chan struct{}
	done     <-chan struct{} // closed once the server or the hook has ended
	client   *http.Client
}

func NewHysteria() *Hysteria {
	return &Hysteria{
		info:    make([]byte, InfoLen),
		config:  hysteriatypes.NewConfig(),
		byAuth:  map[string]string{},
		traffic: map[string]*counters{},
		peers:   common.NewPeerSet(),
		client:  &http.Client{Timeout: apiTimeout},
	}
}

func (s *Hysteria) Type() uint64 {
	return hysteriatypes.Type
}

func (s *Hysteria) Info() []byte {
	return s.info
}

// TLSPin is the SHA-256 of the node's certificate in colon-separated hex, as
// Hysteria2 nodes on the network send it; clients refuse to connect without it.
func (s *Hysteria) TLSPin() string {
	return s.tlsPin
}

func (s *Hysteria) Init(home string) (err error) {
	if _, err = exec.LookPath(binaryName); err != nil {
		return fmt.Errorf("the %q binary is not on PATH: install it on the host "+
			"(docs/operator.md, section 6) or run the Docker image, which bundles it: %w",
			binaryName, err)
	}

	v := viper.New()
	v.SetConfigFile(filepath.Join(home, hysteriatypes.ConfigFileName))

	s.config, err = hysteriatypes.ReadInConfig(v)
	if err != nil {
		return err
	}
	if err = s.config.Validate(); err != nil {
		return err
	}

	data := templateData{
		Server:      s.config.Server,
		API:         s.config.API,
		TLSCertPath: filepath.Join(home, "tls.crt"),
		TLSKeyPath:  filepath.Join(home, "tls.key"),

		BlockedNetworks: common.BlockedNetworks(),
		BlockedDomain:   common.BlockedLocalDomain,
		SMTPPort:        common.SMTPPort,
		AllowSMTP:       common.EgressPolicy().AllowSMTP,
	}

	pin, err := common.CertificatePin(data.TLSCertPath)
	if err != nil {
		return err
	}
	s.tlsPin = common.ColonPin(pin)
	if _, err = os.Stat(data.TLSKeyPath); err != nil {
		return fmt.Errorf("tls key: %w", err)
	}
	// hysteria may run as the proxy account, which cannot read the home.
	if data.TLSCertPath, data.TLSKeyPath, err = common.CurrentRuntime().TLSFiles(home); err != nil {
		return err
	}

	secret := make([]byte, 16)
	if _, err = rand.Read(secret); err != nil {
		return err
	}
	s.statsSecret = hex.EncodeToString(secret)
	data.StatsSecret = s.statsSecret

	t, err := template.New("hysteria_yaml").Funcs(template.FuncMap{"json": jsonString}).Parse(configTemplate)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	if err = t.Execute(&buf, data); err != nil {
		return err
	}
	if s.configPath, err = common.CurrentRuntime().WriteFile("hysteria_config.yaml", buf.Bytes()); err != nil {
		return err
	}

	binary.BigEndian.PutUint16(s.info[0:], s.config.Server.ListenPort)
	if s.config.Server.ObfsPassword != "" {
		s.info[2] = 1
	}

	return nil
}

func jsonString(v string) string {
	b, _ := json.Marshal(v)

	return string(b)
}

// serverArgs is the hysteria command line. Its info level logs every client's
// address as it connects, and its warnings the destinations clients fail to
// reach, so only errors are logged unless the node runs at debug. The update
// check is off: the node runs a pinned build and calls no outside service the
// operator did not choose.
func serverArgs(configPath string) []string {
	level := "error"
	if common.Verbose() {
		level = "info"
	}

	return []string{"server", "--disable-update-check", "--log-level", level, "-c", configPath}
}

// Start serves the authentication hook, then launches the server. The hook
// must be up first: the server may take its first client at once.
func (s *Hysteria) Start() error {
	raiseUDPBuffers()

	lis, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.config.API.AuthPort))
	if err != nil {
		return fmt.Errorf("auth hook: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth", s.handleAuth)
	s.auth = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	s.authDone = make(chan struct{})
	go func() {
		_ = s.auth.Serve(lis)
		close(s.authDone)
	}()

	s.process, err = common.StartProxy(binaryName, serverArgs(s.configPath), nil,
		s.config.Server.ListenPort, s.config.API.AuthPort)
	if err != nil {
		_ = s.auth.Close()

		return err
	}
	s.done = common.EitherDone(s.process.Done(), s.authDone)

	return nil
}

// Done is closed once the server has exited or the authentication hook has
// stopped serving: without the hook the server admits no one.
func (s *Hysteria) Done() <-chan struct{} {
	return s.done
}

// Stop ends the server, then the authentication hook.
func (s *Hysteria) Stop() error {
	if s.process == nil {
		return errors.New("hysteria was not started")
	}

	err := s.process.Stop(stopTimeout)
	if s.auth != nil {
		_ = s.auth.Close()
		<-s.authDone
	}

	return err
}

// raiseUDPBuffers lifts the kernel's socket buffer limits when they are
// below what QUIC wants. Best effort: a container cannot write /proc/sys.
func raiseUDPBuffers() {
	for _, path := range udpBufferSwitches {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		current, err := strconv.Atoi(string(bytes.TrimSpace(raw)))
		if err != nil || current >= udpBufferBytes {
			continue
		}
		_ = os.WriteFile(path, []byte(strconv.Itoa(udpBufferBytes)), 0)
	}
}

// authRequest is what the hysteria server posts for each new client.
type authRequest struct {
	Addr string `json:"addr"`
	Auth string `json:"auth"`
	Tx   uint64 `json:"tx"`
}

type authResponse struct {
	OK bool   `json:"ok"`
	ID string `json:"id,omitempty"`
}

// handleAuth answers the server's authentication call: the client's
// password must be the canonical UUID of a registered peer, and the id the
// server files traffic under is that peer's session key.
func (s *Hysteria) handleAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)

		return
	}

	var req authRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)

		return
	}

	s.mu.Lock()
	key, ok := s.byAuth[req.Auth]
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if !ok {
		_ = json.NewEncoder(w).Encode(authResponse{OK: false})

		return
	}
	_ = json.NewEncoder(w).Encode(authResponse{OK: true, ID: key})
}

func peerKey(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func checkPeerData(data []byte) error {
	if len(data) != common.UUIDLen {
		return fmt.Errorf("peer data must be a %d-byte uuid", common.UUIDLen)
	}

	return nil
}

func (s *Hysteria) AddPeer(data []byte) ([]byte, error) {
	if err := checkPeerData(data); err != nil {
		return nil, err
	}

	var id [common.UUIDLen]byte
	copy(id[:], data)
	key := peerKey(data)

	s.mu.Lock()
	defer s.mu.Unlock()
	// A peer still registered here is one whose kick failed: its open
	// connection is counted until the next pass removes it.
	if s.peers.Has(key) {
		return nil, errors.New("the peer is still registered: its removal has not finished")
	}
	s.byAuth[common.FormatUUID(id)] = key
	s.traffic[key] = &counters{}
	s.peers.Put(key)

	return nil, nil
}

func (s *Hysteria) HasPeer(data []byte) bool {
	return s.peers.Has(peerKey(data))
}

// RemovePeer makes the server refuse the peer's next connection at once and
// kicks any connection it has open. The peer stays registered, and its
// traffic counted, until the kick succeeds: a connection the kick missed
// keeps working, and the next pass removes the peer again.
func (s *Hysteria) RemovePeer(data []byte) error {
	if err := checkPeerData(data); err != nil {
		return err
	}

	var id [common.UUIDLen]byte
	copy(id[:], data)
	key := peerKey(data)

	s.mu.Lock()
	delete(s.byAuth, common.FormatUUID(id))
	s.mu.Unlock()

	body, _ := json.Marshal([]string{key})
	res, err := s.statsAPI(http.MethodPost, "/kick", body)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("kick: status %d", res.StatusCode)
	}

	s.mu.Lock()
	delete(s.traffic, key)
	s.peers.Delete(key)
	s.mu.Unlock()

	return nil
}

// trafficDelta is the server's traffic per id since the last clearing read,
// from the client's point of view: tx = bytes the client sent (its upload),
// rx = bytes it received (its download). Checked against the real server
// with a large download.
type trafficDelta map[string]struct {
	Tx int64 `json:"tx"`
	Rx int64 `json:"rx"`
}

// errBrokenReply is a traffic reply that arrived but could not be decoded.
var errBrokenReply = errors.New("traffic: broken reply")

// readTraffic reads the traffic table; clear also clears it on the server.
func (s *Hysteria) readTraffic(clear bool) (trafficDelta, error) {
	path := "/traffic"
	if clear {
		path += "?clear=1"
	}
	res, err := s.statsAPI(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("traffic: status %d", res.StatusCode)
	}

	var delta trafficDelta
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&delta); err != nil {
		return nil, fmt.Errorf("%w: %v", errBrokenReply, err)
	}

	return delta, nil
}

// Peers reads the traffic since the previous read and reports the totals
// for every registered peer. The server clears its table as it answers a
// clearing read, so a broken reply to one would lose what it carried: the
// table is read once without clearing first, and that reading counts when
// the clearing one arrives broken. Only what moved between the two is then
// lost.
func (s *Hysteria) Peers() ([]types.Peer, error) {
	peek, err := s.readTraffic(false)
	if err != nil {
		return nil, err
	}
	delta, err := s.readTraffic(true)
	if errors.Is(err, errBrokenReply) {
		delta = peek
	} else if err != nil {
		// The server may not have cleared: counting peek could count it
		// twice, so this pass reads nothing.
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for key, d := range delta {
		if c, ok := s.traffic[key]; ok {
			c.upload += d.Tx
			c.download += d.Rx
		}
	}

	keys := s.peers.Keys()
	items := make([]types.Peer, 0, len(keys))
	for _, key := range keys {
		c := s.traffic[key]
		if c == nil {
			c = &counters{}
		}
		items = append(items, types.Peer{Key: key, Upload: c.upload, Download: c.download})
	}

	return items, nil
}

func (s *Hysteria) PeerCount() int {
	return s.peers.Len()
}

// statsAPI calls the server's statistics API; the client's timeout covers
// the whole exchange, body included.
func (s *Hysteria) statsAPI(method, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest(method,
		fmt.Sprintf("http://127.0.0.1:%d%s", s.config.API.StatsPort, path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", s.statsSecret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return s.client.Do(req)
}
