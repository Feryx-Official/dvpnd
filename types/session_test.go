// SPDX-License-Identifier: Apache-2.0

package types

import (
	"math"
	"testing"
	"time"
)

// Rules: [SL-2].
func TestSessionServedBytesAndDuration(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := Session{
		CreatedAt:    start,
		UpdatedAt:    start.Add(90 * time.Second),
		Download:     420,
		Upload:       310,
		BaseDownload: 400,
		BaseUpload:   300,
		BaseDuration: int64(time.Hour),
	}

	if got := s.ServedBytes(); got != 30 {
		t.Fatalf("ServedBytes: got %d, want 30", got)
	}
	if got, want := s.Duration(), time.Hour+90*time.Second; got != want {
		t.Fatalf("Duration: got %s, want %s", got, want)
	}

	// A session admitted before this field existed reports as before.
	var old Session
	old.CreatedAt, old.UpdatedAt = start, start.Add(time.Minute)
	if got := old.Duration(); got != time.Minute {
		t.Fatalf("Duration without base: got %s, want 1m", got)
	}
}

// TestCountersSaturateAtTheCounterLimit: the node keeps byte counters as
// int64 and the chain refuses a total past the largest one. A sum that
// wrapped went negative instead: a reported total below the last one, which
// the chain refuses, and a served count that shrank the allocation an
// admission checks.
//
// Rules: [SL-20].
func TestCountersSaturateAtTheCounterLimit(t *testing.T) {
	s := Session{BaseUpload: math.MaxInt64 - 5, BaseDownload: 7, Upload: math.MaxInt64 - 5, Download: 7}
	upload, download, moved := s.Usage(Peer{Upload: 10, Download: 3})
	if upload != math.MaxInt64 || download != 10 || !moved {
		t.Fatalf("Usage: upload %d download %d moved %v, want %d, 10, true", upload, download, moved, int64(math.MaxInt64))
	}

	served := Session{Download: math.MaxInt64, Upload: 1}
	if got := served.ServedBytes(); got != math.MaxInt64 {
		t.Fatalf("ServedBytes: got %d, want %d", got, int64(math.MaxInt64))
	}
}
