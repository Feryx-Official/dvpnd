// SPDX-License-Identifier: Apache-2.0

package lite

import (
	"testing"
	"time"
)

// TestQueryBlockTime: the time an hourly session's report is bounded at is
// the latest block's, as a remote's status gives it; the block the report
// lands in is never earlier.
//
// Rules: [SL-21].
func TestQueryBlockTime(t *testing.T) {
	chain, c := newFakeChain(t)
	chain.blockTime = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	got, err := c.QueryBlockTime()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(chain.blockTime) {
		t.Fatalf("block time %s, want %s", got, chain.blockTime)
	}
}
