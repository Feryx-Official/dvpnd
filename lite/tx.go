// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package lite

import (
	"fmt"
	"time"

	"github.com/avast/retry-go/v4"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	"github.com/pkg/errors"
)

// waitForTx polls for the transaction until it is included in a block or the
// tx timeout elapses. cosmos-sdk v0.47 removed the "block" broadcast mode that
// upstream relied on, so inclusion has to be confirmed explicitly.
func (c *Client) waitForTx(ctx client.Context, hash string) (*sdk.TxResponse, error) {
	deadline := time.Now().Add(time.Duration(c.txTimeout) * time.Second)
	for {
		res, err := authtx.QueryTx(ctx, hash)
		if err == nil {
			if res.Code != abcitypes.CodeTypeOK {
				return nil, errors.New(res.RawLog)
			}

			return res, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("transaction %s was not included within %d seconds", hash, c.txTimeout)
		}

		time.Sleep(2 * time.Second)
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
		return nil, errors.New(resp.RawLog)
	}
}

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

func (c *Client) tx(messages ...sdk.Msg) (res *sdk.TxResponse, err error) {
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

	txBytes, err := c.TxConfig().TxEncoder()(txb.GetTx())
	if err != nil {
		return nil, err
	}

	c.log.Info("Broadcasting the transaction", "size", len(txBytes))
	res, err = c.BroadcastTx(txBytes)
	if err != nil {
		return nil, err
	}

	return res, nil
}

func (c *Client) Tx(messages ...sdk.Msg) (res *sdk.TxResponse, err error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	messages = c.execMessages(messages)

	err = retry.Do(
		func() error {
			res, err = c.tx(messages...)
			if err != nil {
				return err
			}

			c.log.Info("Transaction result", "code", res.Code,
				"codespace", res.Codespace, "height", res.Height, "tx_hash", res.TxHash)
			return nil
		},
		retry.Attempts(5),
	)
	if err != nil {
		return nil, err
	}

	return res, nil
}
