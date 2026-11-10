// SPDX-License-Identifier: Apache-2.0

// e2e drives a running dvpnd from the client side: it buys a pay-per-session
// slot on the node with the given key, performs the root-path handshake the way
// current client apps do, and writes a WireGuard client configuration for the
// resulting peer. Run it against a node started from the same home directory:
//
//	go run ./tools/e2e -home ~/.dvpnd-test -api https://127.0.0.1:8585
//
// It spends real funds (the session deposit plus gas) on the chain the node is
// configured for. With -node it buys the session on another node instead (any
// sentnode address, with -api pointing at that node's API); the key then only
// pays and signs.
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	cmtlog "github.com/cometbft/cometbft/libs/log"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	base "github.com/sentinel-official/sentinelhub/v12/types"
	v1base "github.com/sentinel-official/sentinelhub/v12/types/v1"
	nodetypes "github.com/sentinel-official/sentinelhub/v12/x/node/types/v3"
	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/lite"
	wgtypes "github.com/Feryx-Official/dvpnd/v9/services/wireguard/types"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

func main() {
	var (
		home       = flag.String("home", types.DefaultHomeDirectory, "node home directory (config.toml, keyring)")
		api        = flag.String("api", "https://127.0.0.1:8585", "node API base URL")
		gigabytes  = flag.Int64("gigabytes", 1, "session size to buy")
		maxPrice   = flag.String("max-price", "", "maximum price accepted (chain notation denom:base,quote); default: the node's own udvpn gigabyte price")
		sessionID  = flag.Uint64("session", 0, "reuse an existing session id instead of buying one")
		deactivate = flag.Bool("deactivate", false, "only send MsgUpdateNodeStatus(inactive) for this key's node and exit")
		out        = flag.String("out", "", "where to write the WireGuard client config (default <home>/e2e-client.conf)")
		endpoint   = flag.String("endpoint", "127.0.0.1", "host written as the WireGuard endpoint in the client config")
		fullTunnel = flag.Bool("full-tunnel", false, "route all client traffic through the node (0.0.0.0/0, ::/0) instead of only its tunnel address")
		nodeType   = flag.String("type", "wireguard", "node type to handshake as: wireguard and amneziawg write a client config; v2ray, xray, hysteria2 and openvpn write the decoded handshake payload")
		nodeStr    = flag.String("node", "", "sentnode address of the node to buy the session on (default: the key's own node)")
		awgVersion = flag.Int("awg-version", 0, "amneziawg only: the tier to ask for (0 = the default tier, 3 = the 3.1 tier)")
	)
	flag.Parse()
	if err := run(*home, *api, *gigabytes, *maxPrice, *sessionID, *out, *deactivate, *endpoint, *fullTunnel, *nodeType, *nodeStr, *awgVersion); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(home, api string, gigabytes int64, maxPriceStr string, sessionID uint64, out string, deactivate bool, endpoint string, fullTunnel bool, nodeType, nodeStr string, awgVersion int) error {
	base.GetConfig().Seal()

	v := viper.New()
	v.SetConfigFile(filepath.Join(home, types.ConfigFileName))
	config, err := types.ReadInConfig(v)
	if err != nil {
		return err
	}

	kr, err := keyring.New(types.KeyringName, config.Keyring.Backend, home, bufio.NewReader(os.Stdin), lite.DefaultEncodingConfig().Codec)
	if err != nil {
		return err
	}
	record, err := kr.Key(config.Keyring.From)
	if err != nil {
		return err
	}
	accAddr, err := record.GetAddress()
	if err != nil {
		return err
	}
	nodeAddr := base.NodeAddress(accAddr.Bytes())
	if nodeStr != "" {
		if deactivate {
			return fmt.Errorf("-deactivate acts on the key's own node; drop -node")
		}
		if nodeAddr, err = base.NodeAddressFromBech32(nodeStr); err != nil {
			return fmt.Errorf("-node: %w", err)
		}
	}
	fmt.Printf("account %s\nnode    %s\n", accAddr, nodeAddr)

	client := lite.NewDefaultClient().
		WithChainID(config.Chain.ID).
		WithFromAddress(accAddr).
		WithFromName(config.Keyring.From).
		WithGas(config.Chain.Gas).
		WithGasAdjustment(config.Chain.GasAdjustment).
		WithGasPrices(config.Chain.GasPrices).
		WithKeyring(kr).
		WithLogger(cmtlog.NewNopLogger()).
		WithQueryTimeout(config.Chain.RPCQueryTimeout).
		WithRemotes(strings.Split(config.Chain.RPCAddresses, ",")).
		WithSignModeStr("").
		WithSimulateAndExecute(config.Chain.SimulateAndExecute).
		WithTxTimeout(config.Chain.RPCTxTimeout)

	if deactivate {
		res, err := client.Tx(nodetypes.NewMsgUpdateNodeStatusRequest(nodeAddr, v1base.StatusInactive))
		if err != nil {
			return fmt.Errorf("MsgUpdateNodeStatus: %w", err)
		}
		fmt.Printf("node set inactive: tx %s height %d\n", res.TxHash, res.Height)
		return nil
	}

	// 1. The node must be registered and active before a session can start.
	node, err := client.QueryNode(nodeAddr)
	if err != nil {
		return err
	}
	if node == nil {
		return fmt.Errorf("node %s is not registered; start dvpnd first", nodeAddr)
	}
	fmt.Printf("on-chain node: status=%s remote_addrs=%v gigabyte_prices=%s\n", node.Status, node.RemoteAddrs, node.GetGigabytePrices())
	if !node.Status.Equal(v1base.StatusActive) {
		return fmt.Errorf("node status is %s; wait for the first status update", node.Status)
	}

	// 2. GET / — what client apps read first.
	http.DefaultClient.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	http.DefaultClient.Timeout = 15 * time.Second
	root, err := http.Get(strings.TrimRight(api, "/") + "/")
	if err != nil {
		return fmt.Errorf("GET /: %w", err)
	}
	rootBody, _ := io.ReadAll(root.Body)
	root.Body.Close()
	fmt.Printf("GET / -> %d %s\n", root.StatusCode, truncate(rootBody, 300))

	// 3. Buy a session on the node (pay-per-session, node module).
	if sessionID == 0 {
		var maxPrice v1base.Price
		if maxPriceStr != "" {
			if maxPrice, err = v1base.NewPriceFromString(maxPriceStr); err != nil {
				return fmt.Errorf("max-price: %w", err)
			}
		} else {
			price, found := node.GigabytePrice("udvpn")
			if !found {
				return fmt.Errorf("node has no udvpn gigabyte price; pass -max-price")
			}
			maxPrice = price
		}
		fmt.Printf("buying %d GB at up to %s (deposit %s udvpn)\n", gigabytes, maxPrice, maxPrice.QuoteValue.MulRaw(gigabytes))
		res, err := client.Tx(nodetypes.NewMsgStartSessionRequest(accAddr, nodeAddr, gigabytes, 0, maxPrice))
		if err != nil {
			return fmt.Errorf("MsgStartSession: %w", err)
		}
		fmt.Printf("MsgStartSession included: tx %s height %d\n", res.TxHash, res.Height)

		sessions, err := client.QuerySessionsForAccount(accAddr)
		if err != nil {
			return err
		}
		for _, s := range sessions {
			if s.GetNodeAddress() == nodeAddr.String() && s.GetStatus().Equal(v1base.StatusActive) && s.GetID() > sessionID {
				sessionID = s.GetID()
			}
		}
		if sessionID == 0 {
			return fmt.Errorf("no active session for %s on %s after the transaction", accAddr, nodeAddr)
		}
	}
	fmt.Printf("session id %d\n", sessionID)

	// 4. Handshake exactly like current client apps: sign BE64(id) || raw JSON.
	// The peer request depends on the node type; the secret the client keeps is
	// a WireGuard private key or the UUID it will present to the proxy.
	wgKey, err := wgtypes.NewPrivateKey()
	if err != nil {
		return err
	}
	peerJSON, clientSecret, err := peerRequest(nodeType, wgKey, awgVersion)
	if err != nil {
		return err
	}
	sig, pubKey, err := kr.Sign(config.Keyring.From, append(sdk.Uint64ToBigEndian(sessionID), peerJSON...))
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]interface{}{
		"data":      base64.StdEncoding.EncodeToString(peerJSON),
		"id":        sessionID,
		"pub_key":   "secp256k1:" + base64.StdEncoding.EncodeToString(pubKey.Bytes()),
		"signature": base64.StdEncoding.EncodeToString(sig),
	})
	resp, err := http.Post(strings.TrimRight(api, "/")+"/", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("POST /: %w", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	fmt.Printf("POST / -> %d %s\n", resp.StatusCode, truncate(respBody, 400))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("handshake refused")
	}

	var envelope struct {
		Result struct {
			Data  string   `json:"data"`
			Addrs []string `json:"addrs"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return err
	}
	dataJSON, err := base64.StdEncoding.DecodeString(envelope.Result.Data)
	if err != nil {
		return err
	}
	signer, err := checkReplySignature(resp.Header.Get("X-Dvpnd-Signature"), sessionID, peerJSON, dataJSON,
		envelope.Result.Addrs, nodeAddr)
	if err != nil {
		return fmt.Errorf("reply signature (X-Dvpnd-Signature): %w", err)
	}
	fmt.Printf("reply signature: valid, signed by %s\n", signer)

	if nodeType == "amneziawg" {
		return writeAmneziaWGConfig(dataJSON, wgKey, envelope.Result.Addrs, home, out, endpoint, fullTunnel)
	}

	if nodeType != "wireguard" {
		// A proxy node: the payload is what a client app builds its outbound
		// from. Write it decoded next to the hosts and the client's uuid, and
		// leave connecting to the protocol's own client.
		if out == "" {
			out = filepath.Join(home, "e2e-handshake.json")
		}
		var pretty bytes.Buffer
		_ = json.Indent(&pretty, dataJSON, "", "  ")
		// data stays raw so the file shows the protocol's own field names
		// unchanged; the encoder quotes everything else for us.
		doc, err := json.MarshalIndent(struct {
			Type  string          `json:"type"`
			UUID  string          `json:"uuid"`
			Addrs []string        `json:"addrs"`
			Data  json.RawMessage `json:"data"`
		}{
			Type:  nodeType,
			UUID:  clientSecret,
			Addrs: envelope.Result.Addrs,
			Data:  dataJSON,
		}, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(out, append(doc, '\n'), 0o600); err != nil {
			return err
		}
		fmt.Printf("handshake payload: %s\n", strings.TrimSpace(pretty.String()))
		fmt.Printf("wrote %s (hosts %v, client uuid %s)\n", out, envelope.Result.Addrs, clientSecret)
		fmt.Println("next: build the protocol's client config from it (docs/protocols.md has the field meanings) and connect.")

		return nil
	}

	var data struct {
		Addrs    []string `json:"addrs"`
		Metadata []struct {
			Port      uint16 `json:"port"`
			PublicKey string `json:"public_key"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(dataJSON, &data); err != nil {
		return err
	}
	if len(data.Metadata) == 0 || len(data.Addrs) == 0 || len(envelope.Result.Addrs) == 0 {
		return fmt.Errorf("handshake data incomplete: %s", dataJSON)
	}
	fmt.Printf("handshake data: addrs=%v port=%d node_pubkey=%s endpoint_hosts=%v\n",
		data.Addrs, data.Metadata[0].Port, data.Metadata[0].PublicKey, envelope.Result.Addrs)

	// 5. A client config. By default it routes only the node's tunnel address, so
	// it can be brought up on the same machine without hijacking its default
	// route; -full-tunnel routes everything through the node and belongs on a
	// separate host or container.
	if out == "" {
		out = filepath.Join(home, "e2e-client.conf")
	}
	allowedIPs := "10.8.0.1/32"
	if fullTunnel {
		allowedIPs = "0.0.0.0/0, ::/0"
	}
	conf := fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s

[Peer]
PublicKey = %s
Endpoint = %s:%d
AllowedIPs = %s
PersistentKeepalive = 15
`, wgKey.String(), strings.Join(data.Addrs, ","), data.Metadata[0].PublicKey, endpoint, data.Metadata[0].Port, allowedIPs)
	if err := os.WriteFile(out, []byte(conf), 0o600); err != nil {
		return err
	}
	fmt.Printf("wrote %s (endpoint %s:%d, allowed ips %s)\n", out, endpoint, data.Metadata[0].Port, allowedIPs)
	fmt.Printf("next (as root): wg-quick up %s && sleep 20 && wg show && wg-quick down %s\n", out, out)
	if fullTunnel {
		fmt.Println("proof: 'curl https://1.1.1.1/cdn-cgi/trace' on the client answers through the node and both 'wg show' outputs count the bytes.")
	} else {
		fmt.Println("proof: a 'latest handshake' line and non-zero 'transfer' on both interfaces. (Pinging 10.8.0.1 from the")
		fmt.Println("same host answers locally and proves nothing.) The node reports usage on-chain at its next update_sessions tick.")
	}

	return nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}

// peerRequest builds the JSON peer request for a node type the way client apps
// do, and returns the secret the client keeps: the WireGuard public key is
// derived from wgKey, the proxy types get a fresh random UUID (sent as a
// 16-byte array, or as the canonical string for hysteria2).
func peerRequest(nodeType string, wgKey *wgtypes.Key, awgVersion int) ([]byte, string, error) {
	switch nodeType {
	case "wireguard", "amneziawg":
		req := map[string]interface{}{"public_key": wgKey.Public().String()}
		if nodeType == "amneziawg" && awgVersion != 0 {
			req["awg_version"] = awgVersion
		}
		peerJSON, _ := json.Marshal(req)
		return peerJSON, wgKey.Public().String(), nil
	case "v2ray", "xray", "openvpn", "hysteria2":
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return nil, "", err
		}
		id[6] = (id[6] & 0x0f) | 0x40
		id[8] = (id[8] & 0x3f) | 0x80
		canonical := fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
		if nodeType == "hysteria2" {
			peerJSON, _ := json.Marshal(map[string]string{"uuid": canonical})
			return peerJSON, canonical, nil
		}
		// encoding/json renders []byte as base64; client apps send a JSON array.
		peerJSON := []byte(fmt.Sprintf(`{"uuid":%s}`, mustJSON(bytesToInts(id[:]))))
		return peerJSON, canonical, nil
	}

	return nil, "", fmt.Errorf("unknown node type %q", nodeType)
}

func bytesToInts(b []byte) []int {
	out := make([]int, len(b))
	for i, v := range b {
		out[i] = int(v)
	}
	return out
}

func mustJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// writeAmneziaWGConfig writes an awg-quick client configuration from an
// AmneziaWG handshake: the WireGuard part plus the parameters the client must
// match (S1-S4, H1-H4, any I1-I5 signature packets, and for the 3.1 tier the
// MTU, header protection key and random trailers). Junk packet counts are the
// client's own choice; fixed values in the usual range are written.
func writeAmneziaWGConfig(dataJSON []byte, wgKey *wgtypes.Key, hosts []string, home, out, endpoint string, fullTunnel bool) error {
	var data struct {
		Addrs    []string                 `json:"addrs"`
		Metadata []map[string]interface{} `json:"metadata"`
	}
	// Numbers stay as written: H1-H4 run up to 2^32 and must not turn into floats.
	dec := json.NewDecoder(bytes.NewReader(dataJSON))
	dec.UseNumber()
	if err := dec.Decode(&data); err != nil {
		return err
	}
	if len(data.Metadata) == 0 || len(data.Addrs) == 0 {
		return fmt.Errorf("handshake data incomplete: %s", dataJSON)
	}
	m := data.Metadata[0]
	fmt.Printf("handshake data: addrs=%v port=%v awg_version=%v endpoint_hosts=%v\n", data.Addrs, m["port"], m["awg_version"], hosts)

	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nAddress = %s\nJc = 4\nJmin = 40\nJmax = 70\n", wgKey.String(), strings.Join(data.Addrs, ","))
	for _, k := range []string{"s1", "s2", "s3", "s4", "h1", "h2", "h3", "h4"} {
		fmt.Fprintf(&b, "%s = %v\n", strings.ToUpper(k), m[k])
	}
	for _, k := range []string{"i1", "i2", "i3", "i4", "i5"} {
		if v, ok := m[k].(string); ok && v != "" {
			fmt.Fprintf(&b, "%s = %s\n", strings.ToUpper(k), v)
		}
	}
	if v, ok := m["awg_version"].(json.Number); ok && v.String() == "3" {
		trailers := "off"
		if t, _ := m["random_trailers"].(bool); t {
			trailers = "on"
		}
		fmt.Fprintf(&b, "MTU = %v\nHeaderProtectionKey = %v\nRandomTrailers = %s\nContentPaddingAddition = 0-32\n", m["mtu"], m["header_protection_key"], trailers)
	}
	allowedIPs := "10.8.0.1/32"
	if fullTunnel {
		allowedIPs = "0.0.0.0/0, ::/0"
	}
	fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %v\nEndpoint = %s:%v\nAllowedIPs = %s\nPersistentKeepalive = 15\n", m["public_key"], endpoint, m["port"], allowedIPs)

	if out == "" {
		out = filepath.Join(home, "e2e-client.conf")
	}
	if err := os.WriteFile(out, []byte(b.String()), 0o600); err != nil {
		return err
	}
	fmt.Printf("wrote %s (endpoint %s:%v, allowed ips %s)\n", out, endpoint, m["port"], allowedIPs)
	fmt.Printf("next (as root): awg-quick up %s && sleep 20 && awg show && awg-quick down %s\n", out, out)

	return nil
}

// checkReplySignature verifies the node's signature over a handshake reply
// (docs/protocols.md, X-Dvpnd-Signature). It is written from the spec, not
// with the node's code, so the two check each other. It returns who signed:
// the node account, or a hot key, which a client must also find granted by
// the node account on the chain.
func checkReplySignature(header string, id uint64, request, reply []byte, addrs []string, node base.NodeAddress) (string, error) {
	rest, ok := strings.CutPrefix(header, "secp256k1:")
	if !ok {
		return "", fmt.Errorf("missing or not a secp256k1 signature: %q", header)
	}
	keyB64, sigB64, ok := strings.Cut(rest, ";")
	if !ok {
		return "", fmt.Errorf("malformed: %q", header)
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil || len(key) != 33 {
		return "", fmt.Errorf("public key: %d bytes, %v", len(key), err)
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != 64 {
		return "", fmt.Errorf("signature: %d bytes, %v", len(sig), err)
	}

	digest := replyDigest(id, request, reply, addrs)
	pub := &secp256k1.PubKey{Key: key}
	if !pub.VerifySignature(digest, sig) {
		return "", errors.New("does not verify over this request and reply")
	}

	signer := sdk.AccAddress(pub.Address())
	if bytes.Equal(signer, node.Bytes()) {
		return "the node account", nil
	}

	return "hot key " + signer.String() + " (valid only with the node account's authz grant)", nil
}

// replyDigest is SHA-256( "dvpnd/handshake-reply/v1" || BE64(id) ||
// SHA-256(request) || SHA-256(reply) || addrs joined by "\n" ).
func replyDigest(id uint64, request, reply []byte, addrs []string) []byte {
	requestHash := sha256.Sum256(request)
	replyHash := sha256.Sum256(reply)

	msg := []byte("dvpnd/handshake-reply/v1")
	msg = binary.BigEndian.AppendUint64(msg, id)
	msg = append(msg, requestHash[:]...)
	msg = append(msg, replyHash[:]...)
	msg = append(msg, strings.Join(addrs, "\n")...)
	digest := sha256.Sum256(msg)

	return digest[:]
}
