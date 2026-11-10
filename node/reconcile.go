// SPDX-License-Identifier: Apache-2.0

package node

import (
	sdkmath "cosmossdk.io/math"
	v1base "github.com/sentinel-official/sentinelhub/v12/types/v1"
	"gorm.io/gorm"

	"github.com/Feryx-Official/dvpnd/v9/types"
)

// chainSessionView is the part of a chain session ReconcileSessions needs; the
// concrete session type from the SDK satisfies it, and a test can fake it.
type chainSessionView interface {
	GetStatus() v1base.Status
	GetUploadBytes() sdkmath.Int
	GetDownloadBytes() sdkmath.Int
}

// ReconcileSessions runs once at startup, before the jobs and the API. A node
// restart brings the VPN service up with a fresh interface, so it has no peers;
// the local session table, however, lives on disk and survives. Every stored
// session therefore now points at a peer that no longer exists. Left in place,
// those rows make admit() reject the client's reconnect with a 409 ("session
// already exists" / "key already exists") until the chain expires the session,
// which strands the client on a dead tunnel.
//
// The peers cannot be revived (their tunnel addresses came from a pool that
// reset on restart), so the correct move is to report whatever usage has not
// yet reached the chain, then clear the table. A reconnect is then admitted
// fresh: the client is handed a new tunnel address and traffic flows again.
func (n *Node) ReconcileSessions() error {
	var items []types.Session
	n.Database().Model(&types.Session{}).Find(&items)
	if len(items) == 0 {
		n.compactDatabase()
		return nil
	}

	n.Log().Info("Reconciling sessions left by a previous run", "count", len(items))

	// Report sessions that are still active on the chain and have moved data
	// since the chain last saw them, so the operator is paid for traffic served
	// before the restart. A chain-query or broadcast failure must not stop the
	// node from starting: the tunnels are dead regardless, so log and go on to
	// clear the table.
	var report []types.Session
	for i := range items {
		session, err := n.Client().QuerySession(items[i].ID)
		if err != nil {
			n.Log().Error("could not query a session while reconciling; skipping its final report",
				"id", items[i].ID, "error", err)
			continue
		}
		if session == nil {
			continue
		}
		if sessionNeedsReport(items[i], session) {
			report = append(report, items[i])
		}
	}
	if len(report) > 0 {
		if err := n.UpdateSessions(report...); err != nil {
			n.Log().Error("could not report sessions while reconciling; clearing them anyway", "error", err)
		}
	}

	if err := n.clearSessions(); err != nil {
		return err
	}
	n.compactDatabase()

	return nil
}

// sessionNeedsReport reports whether a stored session should get a final
// MsgUpdateSession before it is cleared: it must still be active on the chain
// and carry usage the chain has not already recorded.
func sessionNeedsReport(local types.Session, onChain chainSessionView) bool {
	if !onChain.GetStatus().Equal(v1base.StatusActive) {
		return false
	}
	return local.Upload != onChain.GetUploadBytes().Int64() ||
		local.Download != onChain.GetDownloadBytes().Int64()
}

// clearSessions removes every row from the local session table.
func (n *Node) clearSessions() error {
	return n.Database().Session(&gorm.Session{AllowGlobalUpdate: true}).
		Unscoped().Delete(&types.Session{}).Error
}

// compactDatabase rebuilds the database file once the session table is empty.
// The file is opened with secure_delete, but rows deleted by an earlier
// release, before that was on, can still sit in free pages with their wallet
// addresses and peer keys; VACUUM writes the file afresh without them. The
// file holds a few pages, so this is quick, and a failure only gets logged.
func (n *Node) compactDatabase() {
	if err := n.Database().Exec("VACUUM").Error; err != nil {
		n.Log().Error("could not compact the database", "error", err)
	}
}
