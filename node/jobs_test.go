// SPDX-License-Identifier: Apache-2.0

package node

import (
	"testing"

	"github.com/Feryx-Official/dvpnd/v9/types"
)

// A client that reconnects after a node restart keeps its on-chain session;
// the service's counters start again from zero, so the totals must be built
// on what the chain already held or the chain refuses the report.
//
// Rules: [SL-2], [SL-3].
func TestReportedUsage(t *testing.T) {
	cases := []struct {
		name             string
		item             types.Session
		peer             types.Peer
		wantUp, wantDown int64
		wantMoved        bool
	}{
		{"fresh session, nothing moved",
			types.Session{}, types.Peer{}, 0, 0, false},
		{"fresh session, data moved",
			types.Session{}, types.Peer{Upload: 10, Download: 20}, 10, 20, true},
		{"re-admitted session, nothing moved yet",
			types.Session{Upload: 300, Download: 400, BaseUpload: 300, BaseDownload: 400},
			types.Peer{}, 300, 400, false},
		{"re-admitted session, data moved: totals stay above the chain's",
			types.Session{Upload: 300, Download: 400, BaseUpload: 300, BaseDownload: 400},
			types.Peer{Upload: 10, Download: 20}, 310, 420, true},
		{"already stored, counters unchanged",
			types.Session{Upload: 310, Download: 420, BaseUpload: 300, BaseDownload: 400},
			types.Peer{Upload: 10, Download: 20}, 310, 420, false},
		{"only the download moved",
			types.Session{Upload: 310, Download: 420, BaseUpload: 300, BaseDownload: 400},
			types.Peer{Upload: 10, Download: 90}, 310, 490, true},
	}
	for _, c := range cases {
		up, down, moved := c.item.Usage(c.peer)
		if up != c.wantUp || down != c.wantDown || moved != c.wantMoved {
			t.Errorf("%s: got (%d, %d, %v), want (%d, %d, %v)",
				c.name, up, down, moved, c.wantUp, c.wantDown, c.wantMoved)
		}
	}
}

// The usage pass names its columns so a zero download is written too (a
// struct update would skip it as a zero value and leave the old figure).
//
// Rules: [SL-15].
func TestUsageUpdateWritesBothColumns(t *testing.T) {
	n, _, db := usageRig(t,
		[]types.Session{{ID: 1, Key: key(1), Address: "a", Upload: 5, Download: 7}},
		types.Peer{Key: key(1), Upload: 9, Download: 0},
	)

	if err := n.setSessions(); err != nil {
		t.Fatal(err)
	}

	if got := row(t, db, 1); got.Upload != 9 || got.Download != 0 {
		t.Fatalf("stored upload %d download %d, want 9 and 0", got.Upload, got.Download)
	}
}
