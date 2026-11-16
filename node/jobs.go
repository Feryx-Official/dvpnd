// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package node

import (
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	sdkmath "cosmossdk.io/math"
	v1base "github.com/Feryx-Official/feryxhub/v12/types/v1"
	subscriptiontypes "github.com/Feryx-Official/feryxhub/v12/x/subscription/types/v3"

	"github.com/Feryx-Official/dvpnd/v9/lite"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// RetryAfter is how soon a failed job pass is tried again, or the job's interval
// if that is shorter.
const RetryAfter = 15 * time.Second

// runJob runs a job's pass at once and then every interval, for the life of
// the node; the first pass does not wait, because a freshly registered node
// is inactive until its first status update.
//
// A failed pass never stops the node: an RPC outage, a proxy API that does
// not answer, or a session the chain dropped mid-pass is logged and tried
// again after RetryAfter, not a whole interval later, since one failed status
// update would otherwise leave the node without one for two intervals, and the
// chain deactivates a node, ending its leases and plan links, as soon as its
// last update is older than its status_timeout. Stopping instead would cut
// every client's tunnel and restart the node into the same outage. A panic is a bug, and ends the
// node the way a failed API server does, so the VPN service is still stopped
// on the way out.
func (n *Node) runJob(name string, interval time.Duration, pass func() error, errCh chan<- error) {
	defer func() {
		if r := recover(); r != nil {
			errCh <- fmt.Errorf("job %s panicked: %v\n%s", name, r, debug.Stack())
		}
	}()

	n.Log().Info("Starting a job", "name", name, "interval", interval)

	retry := min(interval, n.retryAfter)
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		<-t.C
		start := time.Now()
		next := interval
		if err := pass(); err != nil {
			next = retry
			n.Log().Error(name+" pass failed; retrying soon", "in", retry, "error", err)
		}
		t.Reset(max(next-time.Since(start), 0))
	}
}

// setSessions stores each peer's usage and removes peers the node has no
// session for, or that used up their allocation or the hours their session
// paid for. It runs under the admission lock, so a peer whose handshake is
// still writing its row is not taken for an unknown one, and reads the
// counters under it too: a handshake stores what a peer it evicts moved,
// and figures read before that would overwrite it.
func (n *Node) setSessions() error {
	lock := n.Admission()
	lock.Lock()
	defer lock.Unlock()

	peers, err := n.Service().Peers()
	if err != nil {
		return err
	}

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

		// The checks below run for an idle peer too: it may idle past its
		// hours, or still be connected after a removal that failed.
		moved, err := n.StoreUsage(item, peers[i])
		if err != nil {
			errs = append(errs, err)
		}
		if !moved {
			n.Log().Debug("The peer has not sent any data", "key", types.KeyTag(item.Key),
				"update_at", item.UpdatedAt)
		}

		var (
			available = sdkmath.NewInt(item.Available)
			consumed  = sdkmath.NewInt(peers[i].Upload).Add(sdkmath.NewInt(peers[i].Download))
		)

		if available.IsPositive() && consumed.GT(available) {
			n.Log().Info("Peer allocation exceeded", "key", types.KeyTag(item.Key))
			if err = n.RemovePeer(item.Key); err != nil {
				errs = append(errs, err)
			}
		} else if item.PaidTimeUsed(time.Now()) { // connected until now
			n.Log().Info("Peer paid hours used", "key", types.KeyTag(item.Key))
			if err = n.RemovePeer(item.Key); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errors.Join(errs...)
}

// LastUsagePass stores what the peers moved since the last pass, once more
// before the service stops; the next start reports it (ReconcileSessions).
func (n *Node) LastUsagePass() {
	n.Log().Info("Storing the peers' usage before the service stops")
	if err := n.setSessions(); err != nil {
		n.Log().Error("could not store the peers' usage before the service stops", "error", err)
	}
}

// updateSessions reconciles the local session table with the chain (v3):
// sessions the chain has dropped or deactivated lose their peer; sessions that
// moved data since the last pass are reported with MsgUpdateSession, each
// report cut to what the chain records for its session (boundReport). A
// session with nothing new to report is left alone and the chain expires it
// after its status_timeout, which is the protocol's idle timeout. A session the
// chain cannot be asked about is left for the next pass.
func (n *Node) updateSessions() error {
	var items []types.Session
	if err := n.Database().Model(&types.Session{}).Find(&items).Error; err != nil {
		return err
	}

	count := len(items)
	n.Log().Info("Validating the sessions", "count", count)

	var (
		clock   = n.chainClock()
		reports []types.Report
		errs    []error
	)
	for _, item := range items {
		removePeer, removeSession, skipUpdate, report, err := n.checkSession(item, clock)
		if err != nil {
			errs = append(errs, fmt.Errorf("session %d: %w", item.ID, err))
			skipUpdate = true
		}

		if removePeer || removeSession {
			if err = n.retire(item, removePeer, removeSession); err != nil {
				errs = append(errs, err)
			}
		}

		if !skipUpdate {
			reports = append(reports, report)
		}
	}

	if len(reports) > 0 {
		if err := n.reportSessions(reports); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// chainClock is the latest block time for one pass: asked of the chain the
// first time a report needs it, and then kept.
func (n *Node) chainClock() func() (time.Time, error) {
	var (
		at    time.Time
		err   error
		asked bool
	)

	return func() (time.Time, error) {
		if !asked {
			at, err = n.chain.LatestBlockTime()
			asked = true
		}

		return at, err
	}
}

// checkSession says what to do with a stored session given what the chain
// holds for it, and what to report for it unless skipUpdate.
func (n *Node) checkSession(item types.Session, clock func() (time.Time, error)) (removePeer, removeSession, skipUpdate bool, report types.Report, err error) {
	session, err := n.chain.QuerySession(item.ID)
	if err != nil {
		return false, false, true, report, err
	}

	if session == nil {
		n.Log().Info("Session no longer exists on the chain", "key", types.KeyTag(item.Key), "id", item.ID)
		return true, true, true, report, nil
	}

	report, send, err := boundReport(item, session, clock)
	if err != nil {
		skipUpdate = true
	} else if !send {
		skipUpdate = true
	} else if report.Download != item.Download || report.Upload != item.Upload || report.Duration != item.Duration() {
		n.Log().Info("Cutting a report to what the chain records for the session", "key", types.KeyTag(item.Key),
			"id", item.ID, "download", item.Download, "upload", item.Upload, "duration", item.Duration(),
			"reported_download", report.Download, "reported_upload", report.Upload, "reported_duration", report.Duration)
	}

	// Compared as the chain's integers: a value past int64 would panic.
	if session.GetUploadBytes().Equal(sdkmath.NewInt(item.Upload)) &&
		session.GetDownloadBytes().Equal(sdkmath.NewInt(item.Download)) {
		if item.CreatedAt.Before(session.GetStatusAt()) {
			removePeer = true
		}

		n.Log().Debug("Stale peer connection", "key", types.KeyTag(item.Key),
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
	// A plan session's max_bytes is its allocation's headroom, and zero means
	// none is left; a direct session without max_bytes is an hourly one.
	_, plan := session.(*subscriptiontypes.Session)
	if max := session.GetMaxBytes(); max.IsPositive() || (plan && !max.IsNil()) {
		used := sdkmath.NewInt(item.Upload).Add(sdkmath.NewInt(item.Download))
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
		subscription, qerr := n.chain.QuerySubscription(s.SubscriptionID)
		if qerr != nil {
			return removePeer, removeSession, true, report, qerr
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

	return removePeer, removeSession, skipUpdate, report, err
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

// reportSessions sends MsgUpdateSession for the reports in one transaction.
// One message the chain refuses fails them all: a session the chain deleted
// or ended between the query and the send ("session not found") would
// otherwise hold back every other report on every pass. So when the batch
// fails, each session is asked about again, the ones gone or inactive are
// dropped and the rest sent again; if that fails too, each goes on its own.
// When the chain cannot be asked at all (an RPC outage) the batch is left
// for the next pass.
func (n *Node) reportSessions(items []types.Report) error {
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
		still   []types.Report
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
			n.Log().Info("Not reporting a session the chain has ended", "id", item.ID)
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
