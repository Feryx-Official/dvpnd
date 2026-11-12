// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package types

import (
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Session is a local row for an on-chain session whose peer this node serves.
//
// Download, Upload and the duration the node reports are totals for the whole
// on-chain session, not just for this node process: the chain refuses a
// MsgUpdateSession whose bytes or duration are lower than what it already
// holds, and a session outlives a node restart (the client reconnects with the
// same session id while the VPN service's per-peer counters start again from
// zero). So admission records what the chain held at that moment in
// BaseDownload, BaseUpload and BaseDuration, and the counters the service
// reports are added on top of that.
//
// The row has no soft delete (gorm.Model's DeletedAt): a delete removes it,
// and with it the wallet address and peer key. CreatedAt is when the peer
// was admitted; UpdatedAt is the last write, which gorm sets.
type Session struct {
	ID           uint64 `gorm:"primaryKey;uniqueIndex:idx_sessions_id"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Subscription uint64 `gorm:"index:idx_sessions_subscription_address"`
	Key          string `gorm:"uniqueIndex:idx_sessions_key"`
	Address      string `gorm:"index:idx_sessions_address;index:idx_sessions_subscription_address"`
	Available    int64
	Download     int64
	Upload       int64
	BaseDownload int64
	BaseUpload   int64
	BaseDuration int64 // nanoseconds
	// MaxDuration is the time an hourly session paid for, in nanoseconds; 0
	// for a session without hours. The chain pays such a session for the
	// duration the node reports, up to these hours, so time served beyond
	// them is never paid.
	MaxDuration int64
}

// ServedBytes is what this node process has moved for the session: the
// reported totals minus what the chain already held when the peer was
// admitted.
func (s *Session) ServedBytes() int64 {
	return (s.Download - s.BaseDownload) + (s.Upload - s.BaseUpload)
}

// Usage turns the service's counters for the session's peer, which count
// from the moment the peer was added, into the totals to store and report:
// what the chain held when the peer was admitted plus what moved since. moved
// is false when nothing moved in either direction since the last write.
func (s *Session) Usage(peer Peer) (upload, download int64, moved bool) {
	upload = s.BaseUpload + peer.Upload
	download = s.BaseDownload + peer.Download

	return upload, download, upload != s.Upload || download != s.Download
}

// Duration is the session duration to report: what the chain held at
// admission plus the time this node has served the peer.
func (s *Session) Duration() time.Duration {
	return time.Duration(s.BaseDuration) + s.UpdatedAt.Sub(s.CreatedAt)
}

// PaidTimeUsed says whether the session has used the hours it paid for once
// its peer has been served until at; a session without hours never has.
func (s *Session) PaidTimeUsed(at time.Time) bool {
	return s.MaxDuration > 0 && time.Duration(s.BaseDuration)+at.Sub(s.CreatedAt) >= time.Duration(s.MaxDuration)
}

func (s *Session) GetAddress() sdk.AccAddress {
	if s.Address == "" {
		return nil
	}

	v, err := sdk.AccAddressFromBech32(s.Address)
	if err != nil {
		panic(err)
	}

	return v
}
