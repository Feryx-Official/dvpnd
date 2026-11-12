// SPDX-License-Identifier: Apache-2.0

package types

import (
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
