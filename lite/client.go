// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package lite

import (
	"io"
	"net/http"
	"sync"
	"time"

	cmtlog "github.com/cometbft/cometbft/libs/log"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	jsonrpcclient "github.com/cometbft/cometbft/rpc/jsonrpc/client"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
)

type Client struct {
	ctx          client.Context
	granter      sdk.AccAddress
	log          cmtlog.Logger
	mutex        *sync.Mutex
	queryTimeout uint
	remotes      []string
	txf          tx.Factory
	txTimeout    uint

	rpcs *rpcClients
}

// rpcClients holds one RPC client per remote and timeout, built on first use
// and kept: a client per call opened a connection, and a TLS handshake, for
// every query.
type rpcClients struct {
	sync.Mutex
	m map[rpcKey]*rpchttp.HTTP
}

type rpcKey struct {
	remote  string
	timeout uint
}

// rpcIdleTimeout closes a remote's connection after a quiet spell, so a
// remote the node stopped asking holds none open.
const rpcIdleTimeout = 90 * time.Second

func NewClient() *Client {
	return &Client{
		mutex: &sync.Mutex{},
		rpcs:  &rpcClients{m: map[rpcKey]*rpchttp.HTTP{}},
	}
}

// rpc returns the client for remote whose calls time out after timeout
// seconds.
func (c *Client) rpc(remote string, timeout uint) (*rpchttp.HTTP, error) {
	c.rpcs.Lock()
	defer c.rpcs.Unlock()

	key := rpcKey{remote, timeout}
	if r, ok := c.rpcs.m[key]; ok {
		return r, nil
	}

	hc, err := jsonrpcclient.DefaultHTTPClient(remote)
	if err != nil {
		return nil, err
	}
	hc.Timeout = time.Duration(timeout) * time.Second //nolint:gosec // G115: the operator's timeout in seconds, which wraps only past 292 years
	if t, ok := hc.Transport.(*http.Transport); ok {
		t.IdleConnTimeout = rpcIdleTimeout
	}

	r, err := rpchttp.NewWithClient(remote, "/websocket", hc)
	if err != nil {
		return nil, err
	}
	c.rpcs.m[key] = r

	return r, nil
}

func NewDefaultClient() *Client {
	var (
		cfg = DefaultEncodingConfig()
		ctx = client.Context{}.
			WithBroadcastMode(flags.BroadcastSync).
			WithCodec(cfg.Codec).
			WithInterfaceRegistry(cfg.InterfaceRegistry).
			WithLegacyAmino(cfg.Amino).
			WithOutputFormat("text").
			WithOutput(io.Discard).
			WithSkipConfirmation(true)
	)

	return NewClient().
		WithContext(ctx).
		WithAccountRetriever(authtypes.AccountRetriever{}).
		WithTxConfig(cfg.TxConfig)
}

func (c *Client) WithContext(v client.Context) *Client {
	c.ctx = v
	return c
}

func (c *Client) WithLogger(v cmtlog.Logger) *Client {
	c.log = v
	return c
}

func (c *Client) WithQueryTimeout(v uint) *Client {
	c.queryTimeout = v
	return c
}

func (c *Client) WithRemotes(v []string) *Client {
	c.remotes = v
	return c
}

func (c *Client) WithTxTimeout(v uint) *Client {
	c.txTimeout = v
	return c
}

func (c *Client) WithAccountRetriever(v client.AccountRetriever) *Client {
	c.ctx = c.ctx.WithAccountRetriever(v)
	c.txf = c.txf.WithAccountRetriever(v)
	return c
}

func (c *Client) WithChainID(v string) *Client {
	c.ctx = c.ctx.WithChainID(v)
	c.txf = c.txf.WithChainID(v)
	return c
}

func (c *Client) WithFeeGranterAddress(v sdk.AccAddress) *Client {
	c.ctx = c.ctx.WithFeeGranterAddress(v)
	return c
}

func (c *Client) WithFromAddress(v sdk.AccAddress) *Client {
	c.ctx = c.ctx.WithFromAddress(v)
	return c
}

func (c *Client) WithFromName(v string) *Client {
	c.ctx = c.ctx.WithFromName(v)
	c.txf = c.txf.WithFromName(v) // gas simulation looks the signing key up by name
	return c
}

func (c *Client) WithGas(v uint64) *Client {
	c.txf = c.txf.WithGas(v)
	return c
}

func (c *Client) WithGasAdjustment(v float64) *Client {
	c.txf = c.txf.WithGasAdjustment(v)
	return c
}

func (c *Client) WithGasPrices(v string) *Client {
	c.txf = c.txf.WithGasPrices(v)
	return c
}

func (c *Client) WithKeyring(v keyring.Keyring) *Client {
	c.ctx = c.ctx.WithKeyring(v)
	c.txf = c.txf.WithKeybase(v)
	return c
}

func (c *Client) WithSignModeStr(v string) *Client {
	m := signing.SignMode_SIGN_MODE_UNSPECIFIED
	switch v {
	case flags.SignModeDirect:
		m = signing.SignMode_SIGN_MODE_DIRECT
	case flags.SignModeLegacyAminoJSON:
		m = signing.SignMode_SIGN_MODE_LEGACY_AMINO_JSON
	}

	c.ctx = c.ctx.WithSignModeStr(v)
	c.txf = c.txf.WithSignMode(m)
	return c
}

func (c *Client) WithSimulateAndExecute(v bool) *Client {
	c.txf = c.txf.WithSimulateAndExecute(v)
	return c
}

func (c *Client) WithTxConfig(v client.TxConfig) *Client {
	c.ctx = c.ctx.WithTxConfig(v)
	c.txf = c.txf.WithTxConfig(v)
	return c
}

// SignBytes signs msg with the node's signing key the way the SDK signs
// (secp256k1 ECDSA over SHA-256 of msg) and returns the signature with the
// key's public half.
func (c *Client) SignBytes(msg []byte) ([]byte, cryptotypes.PubKey, error) {
	return c.ctx.Keyring.Sign(c.FromName(), msg)
}

func (c *Client) FromAddress() sdk.AccAddress { return c.ctx.FromAddress }
func (c *Client) FromName() string            { return c.ctx.FromName }
func (c *Client) SimulateAndExecute() bool    { return c.txf.SimulateAndExecute() }
func (c *Client) TxConfig() client.TxConfig   { return c.ctx.TxConfig }
