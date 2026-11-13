// SPDX-License-Identifier: Apache-2.0

package lite

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	cmttypes "github.com/cometbft/cometbft/types"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	sdkquery "github.com/cosmos/cosmos-sdk/types/query"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/feegrant"
)

// fakeChain is a CometBFT RPC endpoint in front of a small model of the
// chain: the client's account, the fee allowances granted to it, a mempool
// that checks each transaction's sequence the way CheckTx does, and blocks
// that include what the mempool took. It answers the gRPC queries the client
// sends through abci_query, the way a node does, and counts the connections
// it accepts.
type fakeChain struct {
	t        *testing.T
	srv      *httptest.Server
	conns    atomic.Int32
	txDecode sdk.TxDecoder

	mu          sync.Mutex
	account     *authtypes.BaseAccount
	allowances  []feegrant.Grant      // in the order the chain lists them
	mempool     []pendingTx           // taken, not yet in a block
	included    map[string]includedTx // by hash
	height      int64
	broadcasts  int
	simulations int

	hold                bool   // keep what the mempool took out of blocks
	landOnAccountQuery  bool   // a held transaction lands just before the next account query
	failCode            uint32 // the code an included transaction ends with
	refuseCode          uint32 // CheckTx refuses every transaction with this code
	loseReplyTo         int    // take the n-th broadcast, then lose the reply
	spendBefore         int    // another wallet's transaction uses the sequence before the n-th broadcast
	staleAccountQueries int    // the first n account queries answer the sequence before the last block's, as a backend a block behind does
}

type pendingTx struct {
	hash string
	tx   []byte
}

type includedTx struct {
	tx     []byte
	height int64
	code   uint32
}

// errLostReply makes the endpoint answer as a proxy whose upstream dropped.
var errLostReply = errors.New("reply lost")

// newFakeChain starts the endpoint and returns it with a client that signs
// for its account.
func newFakeChain(t *testing.T) (*fakeChain, *Client) {
	t.Helper()
	f := &fakeChain{t: t, txDecode: DefaultEncodingConfig().TxConfig.TxDecoder(), included: map[string]includedTx{}}
	f.srv = httptest.NewUnstartedServer(f)
	f.srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			f.conns.Add(1)
		}
	}
	f.srv.Start()
	t.Cleanup(f.srv.Close)

	kr := keyring.NewInMemory(DefaultEncodingConfig().Codec)
	rec, _, err := kr.NewMnemonic("node", keyring.English, sdk.FullFundraiserPath, keyring.DefaultBIP39Passphrase, hd.Secp256k1)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := rec.GetAddress()
	if err != nil {
		t.Fatal(err)
	}
	f.account = authtypes.NewBaseAccount(addr, nil, 7, 40)

	c := NewDefaultClient().
		WithLogger(cmtlog.NewNopLogger()).
		WithRemotes([]string{f.srv.URL}).
		WithQueryTimeout(5).
		WithTxTimeout(1).
		WithChainID("sentinelhub-2").
		WithKeyring(kr).
		WithFromName("node").
		WithFromAddress(addr).
		WithGas(200_000).
		WithGasAdjustment(1.2)

	return f, c
}

func (f *fakeChain) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req rpctypes.RPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	result, err := f.call(req.Method, req.Params)
	f.mu.Unlock()

	if errors.Is(err, errLostReply) {
		http.Error(w, "upstream connection lost", http.StatusBadGateway)
		return
	}
	resp := rpctypes.NewRPCSuccessResponse(req.ID, result)
	if err != nil {
		resp = rpctypes.RPCInternalError(req.ID, err)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *fakeChain) call(method string, params json.RawMessage) (any, error) {
	switch method {
	case "abci_query":
		var p struct {
			Path string            `json:"path"`
			Data cmtbytes.HexBytes `json:"data"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		value, err := f.query(p.Path, p.Data)
		if err != nil {
			return &ctypes.ResultABCIQuery{Response: abci.ResponseQuery{Code: 1, Codespace: "fake", Log: err.Error()}}, nil
		}

		return &ctypes.ResultABCIQuery{Response: abci.ResponseQuery{Value: value}}, nil

	case "broadcast_tx_sync":
		var p struct {
			Tx []byte `json:"tx"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}

		return f.broadcast(p.Tx)

	case "tx":
		var p struct {
			Hash []byte `json:"hash"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		inc, ok := f.included[fmt.Sprintf("%X", p.Hash)]
		if !ok {
			return nil, fmt.Errorf("tx (%X) not found", p.Hash)
		}
		result := abci.ResponseDeliverTx{Code: inc.code}
		if inc.code != 0 {
			result.Codespace, result.Log = "sdk", "out of gas"
		}

		return &ctypes.ResultTx{Hash: p.Hash, Height: inc.height, TxResult: result, Tx: inc.tx}, nil

	case "block":
		var p struct {
			Height string `json:"height"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		height, err := strconv.ParseInt(p.Height, 10, 64)
		if err != nil {
			return nil, err
		}

		return &ctypes.ResultBlock{Block: &cmttypes.Block{Header: cmttypes.Header{Height: height, Time: time.Unix(0, 0).UTC()}}}, nil
	}

	return nil, fmt.Errorf("method %s is not served", method)
}

// broadcast is CheckTx: a transaction already taken is answered as the
// mempool cache answers it, one at another sequence than the account's next
// is refused, and the rest is taken and, unless held, put in a block.
func (f *fakeChain) broadcast(tx []byte) (any, error) {
	f.broadcasts++
	if f.broadcasts == f.spendBefore {
		f.account.Sequence++
	}

	sum := sha256.Sum256(tx)
	hash := fmt.Sprintf("%X", sum)
	if _, ok := f.included[hash]; ok || slices.ContainsFunc(f.mempool, func(p pendingTx) bool { return p.hash == hash }) {
		return nil, errors.New("tx already exists in cache")
	}

	sequence, err := f.sequenceOf(tx)
	if err != nil {
		return nil, err
	}
	result := &ctypes.ResultBroadcastTx{Hash: sum[:]}
	switch next := f.account.Sequence + uint64(len(f.mempool)); {
	case f.refuseCode != 0:
		result.Code, result.Codespace, result.Log = f.refuseCode, "sdk", "insufficient fee"
		return result, nil
	case sequence != next:
		result.Code, result.Codespace = sdkerrors.ErrWrongSequence.ABCICode(), sdkerrors.ErrWrongSequence.Codespace()
		result.Log = fmt.Sprintf("account sequence mismatch, expected %d, got %d: incorrect account sequence", next, sequence)
		return result, nil
	}

	f.mempool = append(f.mempool, pendingTx{hash: hash, tx: tx})
	if !f.hold {
		f.release()
	}
	if f.broadcasts == f.loseReplyTo {
		return nil, errLostReply
	}

	return result, nil
}

// sequenceOf reads the sequence a transaction was signed at.
func (f *fakeChain) sequenceOf(tx []byte) (uint64, error) {
	decoded, err := f.txDecode(tx)
	if err != nil {
		return 0, err
	}
	signed, ok := decoded.(authsigning.SigVerifiableTx)
	if !ok {
		return 0, errors.New("not a signed transaction")
	}
	sigs, err := signed.GetSignaturesV2()
	if err != nil || len(sigs) != 1 {
		return 0, fmt.Errorf("%d signatures: %v", len(sigs), err)
	}

	return sigs[0].Sequence, nil
}

// release puts what the mempool took into blocks. Each transaction uses the
// account's sequence and pays its fee whatever its code, as on the chain.
func (f *fakeChain) release() {
	for _, p := range f.mempool {
		f.height++
		f.included[p.hash] = includedTx{tx: p.tx, height: f.height, code: f.failCode}
		f.account.Sequence++
	}
	f.mempool = nil
}

// landHeld puts what the mempool still holds into blocks.
func (f *fakeChain) landHeld() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.release()
}

// paid is how many transactions blocks included: one fee each.
func (f *fakeChain) paid() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.included)
}

// query answers one gRPC query with the response's protobuf encoding.
func (f *fakeChain) query(path string, data []byte) ([]byte, error) {
	switch path {
	case "/cosmos.auth.v1beta1.Query/Account":
		if f.landOnAccountQuery {
			f.release()
		}
		answer := f.account
		if f.staleAccountQueries > 0 {
			f.staleAccountQueries--
			behind := *f.account
			behind.Sequence--
			answer = &behind
		}
		account, err := codectypes.NewAnyWithValue(answer)
		if err != nil {
			return nil, err
		}

		return (&authtypes.QueryAccountResponse{Account: account}).Marshal()

	case "/cosmos.feegrant.v1beta1.Query/Allowances":
		var req feegrant.QueryAllowancesRequest
		if err := req.Unmarshal(data); err != nil {
			return nil, err
		}

		return f.allowancePage(req.Pagination)

	case "/cosmos.tx.v1beta1.Service/Simulate":
		// Each estimate differs a little, as gas used does between blocks.
		f.simulations++
		gas := &sdk.GasInfo{GasUsed: 100_000 + uint64(f.simulations)}

		return (&txtypes.SimulateResponse{GasInfo: gas, Result: &sdk.Result{}}).Marshal()
	}

	return nil, fmt.Errorf("query %s is not served", path)
}

// allowancePage serves one page of the allowances, as the chain does: 100
// when the request names no limit, and a key for the next page while there
// is one.
func (f *fakeChain) allowancePage(page *sdkquery.PageRequest) ([]byte, error) {
	start, limit := 0, 100
	if page != nil {
		if page.Limit > 0 {
			limit = int(page.Limit)
		}
		if len(page.Key) > 0 {
			n, err := strconv.Atoi(string(page.Key))
			if err != nil {
				return nil, errors.New("invalid page key")
			}
			start = n
		}
	}
	end := min(start+limit, len(f.allowances))

	resp := &feegrant.QueryAllowancesResponse{Pagination: &sdkquery.PageResponse{}}
	for i := start; i < end; i++ {
		resp.Allowances = append(resp.Allowances, &f.allowances[i])
	}
	if end < len(f.allowances) {
		resp.Pagination.NextKey = []byte(strconv.Itoa(end))
	}

	return resp.Marshal()
}

// grantAllowance records an allowance from granter to the client's account.
func (f *fakeChain) grantAllowance(granter sdk.AccAddress, allowance feegrant.FeeAllowanceI) {
	f.t.Helper()
	grant, err := feegrant.NewGrant(granter, f.account.GetAddress(), allowance)
	if err != nil {
		f.t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allowances = append(f.allowances, grant)
}
