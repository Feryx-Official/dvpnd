// SPDX-License-Identifier: Apache-2.0

package lite

import (
	"errors"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	base "github.com/sentinel-official/sentinelhub/v12/types"
	v1base "github.com/sentinel-official/sentinelhub/v12/types/v1"
	nodetypes "github.com/sentinel-official/sentinelhub/v12/x/node/types/v3"
)

// quickTx shortens the waits between polls and attempts for the test.
func quickTx(t *testing.T) {
	poll, retry := txPollInterval, txRetryDelay
	txPollInterval, txRetryDelay = 20*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { txPollInterval, txRetryDelay = poll, retry })
}

func statusMsg(c *Client) sdk.Msg {
	return nodetypes.NewMsgUpdateNodeStatusRequest(base.NodeAddress(c.FromAddress().Bytes()), v1base.StatusActive)
}

// TestOneCallPaysForOneTransactionAtMost: whatever the chain answers, one Tx
// call gets at most one transaction into a block, and pays one fee. A
// transaction a block included is never sent again, whatever its code; one a
// remote took but no block included in time is left to land; a lost reply is
// answered by sending the same bytes again, not a new transaction.
//
// Rules: [CH-11].
func TestOneCallPaysForOneTransactionAtMost(t *testing.T) {
	quickTx(t)
	for _, tc := range []struct {
		name     string
		set      func(*fakeChain)
		simulate bool
		check    func(*testing.T, *sdk.TxResponse, error)
	}{
		{
			name: "a block included it with a failure code",
			set:  func(f *fakeChain) { f.failCode = 11 },
			check: func(t *testing.T, _ *sdk.TxResponse, err error) {
				var failed *FailedTxError
				if !errors.As(err, &failed) || failed.Code != 11 {
					t.Errorf("err = %v, want the failure the block recorded", err)
				}
			},
		},
		{
			name: "no block included it in time and it lands later",
			set:  func(f *fakeChain) { f.hold, f.landOnAccountQuery = true, true },
			check: func(t *testing.T, _ *sdk.TxResponse, err error) {
				if !errors.Is(err, ErrUnconfirmed) {
					t.Errorf("err = %v, want ErrUnconfirmed", err)
				}
			},
		},
		{
			name:     "the reply to the broadcast was lost",
			set:      func(f *fakeChain) { f.loseReplyTo = 1 },
			simulate: true,
			check: func(t *testing.T, res *sdk.TxResponse, err error) {
				if err != nil || res == nil || res.Code != 0 || res.Height == 0 {
					t.Errorf("res = %v, err = %v: the transaction the chain took is the result", res, err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chain, c := newFakeChain(t)
			tc.set(chain)
			c.WithSimulateAndExecute(tc.simulate)

			res, err := c.Tx(statusMsg(c))
			chain.landHeld()
			if n := chain.paid(); n != 1 {
				t.Errorf("one call got %d transactions into blocks, want 1", n)
			}
			tc.check(t, res, err)
		})
	}
}

// TestARefusedTransactionIsBuiltAgainOnlyForItsSequence: a transaction a
// remote refused at CheckTx cost nothing. Refused because another transaction
// used its sequence, it is built again at the account's sequence and goes
// through; refused for anything else, sending it again changes nothing.
//
// Rules: [CH-11].
func TestARefusedTransactionIsBuiltAgainOnlyForItsSequence(t *testing.T) {
	quickTx(t)

	chain, c := newFakeChain(t)
	chain.spendBefore = 1
	res, err := c.Tx(statusMsg(c))
	if err != nil || res.Code != 0 {
		t.Fatalf("res = %v, err = %v: the transaction built again must go through", res, err)
	}
	if chain.broadcasts != 2 || chain.paid() != 1 {
		t.Fatalf("%d broadcasts, %d in blocks; want 2 and 1", chain.broadcasts, chain.paid())
	}

	chain, c = newFakeChain(t)
	chain.refuseCode = 13
	_, err = c.Tx(statusMsg(c))
	var refused *RefusedTxError
	if !errors.As(err, &refused) || refused.Code != 13 {
		t.Fatalf("err = %v, want the remote's refusal", err)
	}
	if chain.broadcasts != 1 {
		t.Fatalf("a refusal another send cannot change was sent %d times", chain.broadcasts)
	}
}
