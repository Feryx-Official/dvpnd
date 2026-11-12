// SPDX-License-Identifier: Apache-2.0

package lite

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkquery "github.com/cosmos/cosmos-sdk/types/query"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/feegrant"
)

// fakeChain is a CometBFT RPC endpoint in front of a small model of the
// chain: the client's account, and the fee allowances granted to it. It
// answers the gRPC queries the client sends through abci_query, the way a
// node does.
type fakeChain struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	account    *authtypes.BaseAccount
	allowances []feegrant.Grant // in the order the chain lists them
}

// newFakeChain starts the endpoint and returns it with a client that signs
// for its account.
func newFakeChain(t *testing.T) (*fakeChain, *Client) {
	t.Helper()
	f := &fakeChain{t: t}
	f.srv = httptest.NewServer(f)
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
		WithGas(200_000)

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
	}

	return nil, fmt.Errorf("method %s is not served", method)
}

// query answers one gRPC query with the response's protobuf encoding.
func (f *fakeChain) query(path string, data []byte) ([]byte, error) {
	switch path {
	case "/cosmos.auth.v1beta1.Query/Account":
		account, err := codectypes.NewAnyWithValue(f.account)
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
