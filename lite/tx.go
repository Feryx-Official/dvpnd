// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package lite

import (
	"errors"
	"fmt"
	"time"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
)

// ErrUnconfirmed: a remote took the transaction but no block included it
// within rpc_tx_timeout. It may still land, so it is neither sent again nor
// replaced by a new one.
var ErrUnconfirmed = errors.New("the transaction was taken but no block included it")

// FailedTxError is a transaction a block included with a non-zero code: its
// fee is paid and its messages had no effect.
type FailedTxError struct {
	Hash      string
	Code      uint32
	Codespace string
	Log       string
}

func (e *FailedTxError) Error() string {
	return fmt.Sprintf("transaction %s failed with code %d (%s): %s", e.Hash, e.Code, e.Codespace, e.Log)
}

// RefusedTxError is a transaction a remote refused at CheckTx, before any
// block: nothing was spent.
type RefusedTxError struct {
	Code      uint32
	Codespace string
	Log       string
}

func (e *RefusedTxError) Error() string { return e.Log }

// staleSequence reports a refusal because another transaction already used
// the sequence the transaction was signed at.
func (e *RefusedTxError) staleSequence() bool {
	return e.Codespace == sdkerrors.ErrWrongSequence.Codespace() && e.Code == sdkerrors.ErrWrongSequence.ABCICode()
}

// settled reports an answer another remote cannot change: a block included
// the transaction, or a remote took it and it may still land.
func settled(err error) bool {
	var failed *FailedTxError

	return errors.Is(err, ErrUnconfirmed) || errors.As(err, &failed)
}

// refusal is the refusal every remote gave the transaction, or nil when one
// of them may hold it: a remote that did not answer may have taken it.
func refusal(err error) *RefusedTxError {
	var refused *RefusedTxError
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			if !errors.As(e, &refused) {
				return nil
			}
		}

		return refused
	}
	if errors.As(err, &refused) {
		return refused
	}

	return nil
}

// txAttempts is how many times Tx sends a transaction before it gives up.
const txAttempts = 5

var (
	// txPollInterval is how often the client asks whether a block included
	// the transaction.
	txPollInterval = 2 * time.Second
	// txRetryDelay is the pause before a transaction is sent again.
	txRetryDelay = 500 * time.Millisecond
)

// waitForTx polls for the transaction until it is included in a block or the
// tx timeout elapses. cosmos-sdk v0.47 removed the "block" broadcast mode that
// upstream relied on, so inclusion has to be confirmed explicitly.
func (c *Client) waitForTx(ctx client.Context, hash string) (*sdk.TxResponse, error) {
	deadline := time.Now().Add(time.Duration(c.txTimeout) * time.Second) //nolint:gosec // G115: the operator's timeout in seconds, which wraps only past 292 years
	for {
		res, err := authtx.QueryTx(ctx, hash)
		if err == nil {
			if res.Code != abcitypes.CodeTypeOK {
				return nil, &FailedTxError{Hash: hash, Code: res.Code, Codespace: res.Codespace, Log: res.RawLog}
			}

			return res, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: transaction %s, after %d seconds", ErrUnconfirmed, hash, c.txTimeout)
		}

		time.Sleep(txPollInterval)
	}
}

func (c *Client) broadcastTx(remote string, txBytes []byte) (*sdk.TxResponse, error) {
	c.log.Debug("Broadcasting the transaction", "remote", remote, "size", len(txBytes))

	client, err := c.rpc(remote, c.txTimeout)
	if err != nil {
		return nil, err
	}

	ctx := c.ctx.WithClient(client)

	resp, err := ctx.BroadcastTx(txBytes)
	if err != nil {
		return nil, err
	}

	switch resp.Code {
	case abcitypes.CodeTypeOK, sdkerrors.ErrTxInMempoolCache.ABCICode():
		return c.waitForTx(ctx, resp.TxHash)
	default:
		return nil, &RefusedTxError{Code: resp.Code, Codespace: resp.Codespace, Log: resp.RawLog}
	}
}

// BroadcastTx sends txBytes to each remote in turn until a block includes
// it, and stops early once the chain's answer is settled.
func (c *Client) BroadcastTx(txBytes []byte) (res *sdk.TxResponse, err error) {
	defer func() {
		if err != nil {
			c.log.Error("failed to broadcast the transaction", "error", err)
		}
	}()

	err = c.eachRemote("Broadcast failed", func(remote string) (err error) {
		res, err = c.broadcastTx(remote, txBytes)
		return err
	})
	if err != nil {
		return nil, err
	}

	return res, nil
}

func (c *Client) calculateGas(remote string, txf tx.Factory, messages ...sdk.Msg) (uint64, error) {
	c.log.Debug("Calculating the gas", "remote", remote, "messages", len(messages))

	client, err := c.rpc(remote, c.txTimeout)
	if err != nil {
		return 0, err
	}

	ctx := c.ctx.WithClient(client)

	_, gas, err := tx.CalculateGas(ctx, txf, messages...)
	if err != nil {
		return 0, err
	}

	return gas, nil
}

func (c *Client) CalculateGas(txf tx.Factory, messages ...sdk.Msg) (gas uint64, err error) {
	err = c.eachRemote("Gas calculation failed", func(remote string) (err error) {
		gas, err = c.calculateGas(remote, txf, messages...)
		return err
	})
	if err != nil {
		return 0, err
	}

	return gas, nil
}

func (c *Client) PrepareTxFactory(messages ...sdk.Msg) (txf tx.Factory, err error) {
	defer func() {
		if err != nil {
			c.log.Error("failed to prepare the transaction", "error", err)
		}
	}()

	acc, err := c.QueryAccount(c.FromAddress())
	if err != nil {
		return txf, err
	}
	if acc == nil {
		return txf, fmt.Errorf("account %s does not exist", c.FromAddress())
	}

	txf = c.txf.
		WithAccountNumber(acc.GetAccountNumber()).
		WithSequence(acc.GetSequence())
	if c.granter != nil {
		txf = txf.WithFeeGranter(c.granter)
	}

	if c.SimulateAndExecute() {
		gas, err := c.CalculateGas(txf, messages...)
		if err != nil {
			return txf, err
		}

		txf = txf.WithGas(gas)
	}

	return txf, nil
}

// signedTx builds the transaction at the account's current sequence and
// signs it.
func (c *Client) signedTx(messages ...sdk.Msg) ([]byte, error) {
	c.log.Info("Preparing the transaction", "messages", len(messages))
	txf, err := c.PrepareTxFactory(messages...)
	if err != nil {
		return nil, err
	}

	c.log.Info("Transaction info", "gas", txf.Gas(), "sequence", txf.Sequence())
	txb, err := txf.BuildUnsignedTx(messages...)
	if err != nil {
		return nil, err
	}

	if err = tx.Sign(txf, c.FromName(), txb, true); err != nil {
		return nil, err
	}

	return c.TxConfig().TxEncoder()(txb.GetTx())
}

// Tx signs the messages into one transaction and sends it until a block
// includes it, paying for one transaction at most. A transaction a block
// included is never sent again, whatever its code, and one a remote took is
// left to land. When no remote answers, the same bytes go again: a remote
// may have taken them, and a new transaction would be a second one. Only a
// refusal for a sequence another transaction used builds a new one.
func (c *Client) Tx(messages ...sdk.Msg) (*sdk.TxResponse, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	messages = c.execMessages(messages)

	var (
		txBytes []byte // the transaction sent last, while a remote may hold it
		errs    []error
	)
	for attempt := 0; attempt < txAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(txRetryDelay)
		}
		if txBytes == nil {
			b, err := c.signedTx(messages...)
			if err != nil {
				errs = append(errs, err) // nothing was sent
				continue
			}
			txBytes = b
		}

		c.log.Info("Broadcasting the transaction", "size", len(txBytes))
		res, err := c.BroadcastTx(txBytes)
		if err == nil {
			c.log.Info("Transaction result", "code", res.Code,
				"codespace", res.Codespace, "height", res.Height, "tx_hash", res.TxHash)
			return res, nil
		}
		errs = append(errs, err)

		refused := refusal(err)
		switch {
		case settled(err):
			return nil, err
		case refused == nil:
			// No remote answered, and one may hold the transaction.
		case refused.staleSequence():
			txBytes = nil
		default:
			return nil, err
		}
	}

	return nil, fmt.Errorf("after %d attempts: %w", txAttempts, errors.Join(errs...))
}
