// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package node

import (
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	sdkmath "cosmossdk.io/math"
	v1base "github.com/sentinel-official/sentinelhub/v12/types/v1"
	subscriptiontypes "github.com/sentinel-official/sentinelhub/v12/x/subscription/types/v3"

	"github.com/Feryx-Official/dvpnd/v9/lite"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// runJob runs a job's pass at once and then at every tick, for the life of
// the node; the first pass does not wait, because a freshly registered node
// is inactive until its first status update.
//
// A failed pass never stops the node: an RPC outage, a proxy API that does
// not answer, or a session the chain dropped mid-pass is logged and tried
// again at the next tick. Stopping instead would cut every client's tunnel
// and restart the node into the same outage. A panic is a bug, and ends the
// node the way a failed API server does, so the VPN service is still stopped
// on the way out.
func (n *Node) runJob(name string, interval time.Duration, pass func() error, errCh chan<- error) {
	defer func() {
		if r := recover(); r != nil {
			errCh <- fmt.Errorf("job %s panicked: %v\n%s", name, r, debug.Stack())
		}
	}()

	n.Log().Info("Starting a job", "name", name, "interval", interval)

	t := time.NewTicker(interval)
	defer t.Stop()
	for ; ; <-t.C {
		if err := pass(); err != nil {
			n.Log().Error(name+" pass failed; retrying at the next tick", "error", err)
		}
	}
}

// setSessions stores each peer's usage and removes peers the node has no
// session for, or that used up their allocation or the hours their session
// paid for. It runs under the admission lock, so a peer whose handshake is
// still writing its row is not taken for an unknown one.
func (n *Node) setSessions() error {
	peers, err := n.Service().Peers()
	if err != nil {
		return err
	}

	lock := n.Admission()
	lock.Lock()
	defer lock.Unlock()

	count := len(peers)
	n.Log().Debug("Validating the peers", "count", count)

	var errs []error
	for i := 0; i < count; i++ {
		var items []types.Session
		err = n.Database().Model(&types.Session{}).Where(&types.Session{Key: peers[i].Key}).Limit(1).Find(&items).Error
		if err != nil {
			errs = append(errs, err)
			continue
		}

		if len(items) == 0 {
			n.Log().Info("Unknown connected peer", "key", types.KeyTag(peers[i].Key))
			if err = n.RemovePeer(peers[i].Key); err != nil {
				errs = append(errs, err)
			}

			continue
		}
		item := items[0]

		upload, download, moved := reportedUsage(item, peers[i])
		if !moved {
			n.Log().Debug("The peer has not sent any data", "key", types.KeyTag(item.Key),
				"update_at", item.UpdatedAt)
			continue
		}

		err = n.Database().Model(&types.Session{}).Where(&types.Session{ID: item.ID}).Updates(
			map[string]interface{}{
				"upload":   upload,
				"download": download,
			},
		).Error
		if err != nil {
			errs = append(errs, err)
		}

		var (
			available = sdkmath.NewInt(item.Available)
			consumed  = sdkmath.NewInt(peers[i].Upload + peers[i].Download)
		)

		if available.IsPositive() && consumed.GT(available) {
			n.Log().Info("Peer allocation exceeded", "key", types.KeyTag(item.Key))
			if err = n.RemovePeer(item.Key); err != nil {
				errs = append(errs, err)
			}
		} else if item.PaidTimeUsed(time.Now()) { // served until now: the update set updated_at
			n.Log().Info("Peer paid hours used", "key", types.KeyTag(item.Key))
			if err = n.RemovePeer(item.Key); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errors.Join(errs...)
}

// reportedUsage turns the service's per-peer counters, which count from the
// moment the peer was added, into the session totals to store and report:
// what the chain held when the peer was admitted plus what moved since. moved
// is false when nothing moved in either direction since the last pass.
func reportedUsage(item types.Session, peer types.Peer) (upload, download int64, moved bool) {
	upload = item.BaseUpload + peer.Upload
	download = item.BaseDownload + peer.Download

	return upload, download, upload != item.Upload || download != item.Download
}

// updateSessions reconciles the local session table with the chain (v3):
// sessions the chain has dropped or deactivated lose their peer; sessions that
// moved data since the last pass are reported with MsgUpdateSession. A session
// that has not moved data is left alone and the chain expires it after its
// status_timeout, which is the protocol's idle timeout. A session the chain
// cannot be asked about is left for the next pass.
func (n *Node) updateSessions() error {
	var items []types.Session
	if err := n.Database().Model(&types.Session{}).Find(&items).Error; err != nil {
		return err
	}

	count := len(items)
	n.Log().Info("Validating the sessions", "count", count)

	var errs []error
	for i := count - 1; i >= 0; i-- {
		removePeer, removeSession, skipUpdate, err := n.checkSession(items[i])
		if err != nil {
			errs = append(errs, fmt.Errorf("session %d: %w", items[i].ID, err))
			skipUpdate = true
		}

		if removePeer || removeSession {
			if err = n.retire(items[i], removePeer, removeSession); err != nil {
				errs = append(errs, err)
			}
		}

		if skipUpdate {
			items = append(items[:i], items[i+1:]...)
		}
	}

	if len(items) > 0 {
		if err := n.reportSessions(items); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// checkSession says what to do with a stored session given what the chain
// holds for it.
func (n *Node) checkSession(item types.Session) (removePeer, removeSession, skipUpdate bool, err error) {
	session, err := n.chain.QuerySession(item.ID)
	if err != nil {
		return false, false, true, err
	}

	if session == nil {
		n.Log().Info("Session no longer exists on the chain", "key", types.KeyTag(item.Key), "id", item.ID)
		return true, true, true, nil
	}

	// Compared as the chain's integers: a value past int64 would panic.
	if session.GetUploadBytes().Equal(sdkmath.NewInt(item.Upload)) &&
		session.GetDownloadBytes().Equal(sdkmath.NewInt(item.Download)) {
		skipUpdate = true
		if item.CreatedAt.Before(session.GetStatusAt()) {
			removePeer = true
		}

		n.Log().Info("Stale peer connection", "key", types.KeyTag(item.Key),
			"created_at", item.CreatedAt, "status_at", session.GetStatusAt())
	}
	if !session.GetStatus().Equal(v1base.StatusActive) {
		removePeer = true
		if session.GetStatus().Equal(v1base.StatusInactive) {
			removeSession, skipUpdate = true, true
		}

		n.Log().Info("Invalid session status", "key", types.KeyTag(item.Key),
			"id", session.GetID(), "status", session.GetStatus())
	}
	if max := session.GetMaxBytes(); max.IsPositive() {
		used := sdkmath.NewInt(item.Upload + item.Download)
		if used.GTE(max) {
			removePeer = true
			n.Log().Info("Session byte limit reached", "key", types.KeyTag(item.Key),
				"id", session.GetID(), "max_bytes", max, "used", used)
		}
	}
	// The chain's max_duration also covers a row written before the node
	// stored it.
	if max := session.GetMaxDuration(); max > 0 && item.Duration() >= max {
		removePeer = true
		n.Log().Info("Session paid hours used", "key", types.KeyTag(item.Key),
			"id", session.GetID(), "max_duration", max, "duration", item.Duration())
	}
	if s, ok := session.(*subscriptiontypes.Session); ok {
		subscription, err := n.chain.QuerySubscription(s.SubscriptionID)
		if err != nil {
			return removePeer, removeSession, true, err
		}
		if subscription == nil || !subscription.Status.Equal(v1base.StatusActive) {
			removePeer = true
			if subscription == nil || subscription.Status.Equal(v1base.StatusInactive) {
				removeSession, skipUpdate = true, true
			}

			n.Log().Info("Invalid subscription status", "key", types.KeyTag(item.Key),
				"id", s.SubscriptionID)
		}
	}

	return removePeer, removeSession, skipUpdate, nil
}

// retire removes a session's peer, its row, or both, under the admission
// lock.
func (n *Node) retire(item types.Session, removePeer, removeSession bool) error {
	lock := n.Admission()
	lock.Lock()
	defer lock.Unlock()

	if removePeer {
		if err := n.RemovePeerIfExists(item.Key); err != nil {
			return err
		}
	}
	if removeSession {
		return n.Database().Model(&types.Session{}).Where(&types.Session{ID: item.ID}).
			Unscoped().Delete(&types.Session{}).Error
	}

	return nil
}

// reportSessions sends MsgUpdateSession for the sessions in one transaction.
// One message the chain refuses fails them all: a session the chain deleted
// or ended between the query and the send ("session not found") would
// otherwise hold back every other report on every pass. So when the batch
// fails, each session is asked about again, the ones gone or inactive are
// dropped and the rest sent again; if that fails too, each goes on its own.
// When the chain cannot be asked at all (an RPC outage) the batch is left
// for the next pass.
func (n *Node) reportSessions(items []types.Session) error {
	err := n.chain.UpdateSessions(items...)
	if err == nil {
		return nil
	}
	if errors.Is(err, lite.ErrUnconfirmed) {
		// The batch may still land: sending its sessions again would pay
		// for their reports twice. The next pass sees what the chain holds.
		return err
	}
	n.Log().Info("Session report failed; checking each session again", "count", len(items), "error", err)

	var (
		still   []types.Session
		queried int
	)
	for _, item := range items {
		session, qerr := n.chain.QuerySession(item.ID)
		if qerr != nil {
			still = append(still, item)
			continue
		}
		queried++
		if session == nil || session.GetStatus().Equal(v1base.StatusInactive) {
			n.Log().Info("Not reporting a session the chain has ended", "key", types.KeyTag(item.Key), "id", item.ID)
			continue
		}
		still = append(still, item)
	}

	switch {
	case len(still) == 0:
		return nil
	case queried == 0:
		return err
	case len(still) < len(items):
		if err = n.chain.UpdateSessions(still...); err == nil {
			return nil
		}
	}

	var errs []error
	for _, item := range still {
		if err := n.chain.UpdateSessions(item); err != nil {
			errs = append(errs, fmt.Errorf("session %d: %w", item.ID, err))
		}
	}

	return errors.Join(errs...)
}
