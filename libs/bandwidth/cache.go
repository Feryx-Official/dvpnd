// SPDX-License-Identifier: Apache-2.0

package bandwidth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// The measurement moves gigabytes and takes a minute or two, and a restart in
// the middle of a speed-test outage must not turn a fast node into one that
// advertises nothing. So a successful measurement is kept on disk and reused
// for cacheTTL, or for any length of time when a fresh one fails. It is keyed
// by the public address so a node moved to another host measures again.
const (
	cacheFileName = "bandwidth.json"
	cacheTTL      = 7 * 24 * time.Hour
)

type cacheEntry struct {
	IP         string    `json:"ip"`
	Upload     int64     `json:"upload"`   // bytes per second
	Download   int64     `json:"download"` // bytes per second
	MeasuredAt time.Time `json:"measured_at"`
}

func (e *cacheEntry) result() *Result {
	return &Result{Upload: e.Upload, Download: e.Download, Source: SourceSpeedtest}
}

// readCache returns the stored measurement, or nil when there is none, the
// cache is disabled, or the file cannot be read (which is logged and then
// treated as absent).
func readCache(home string, log Logger) *cacheEntry {
	if home == "" {
		return nil
	}

	path := filepath.Join(home, cacheFileName)
	data, err := os.ReadFile(path) //nolint:gosec // G304: a fixed name in the node's home
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		log.Error("Could not read the cached bandwidth measurement", "path", path, "error", err)
		return nil
	}

	var e cacheEntry
	if err := json.Unmarshal(data, &e); err != nil || e.Upload <= 0 || e.Download <= 0 || e.MeasuredAt.IsZero() {
		log.Error("Ignoring the cached bandwidth measurement: unreadable", "path", path, "error", err)
		return nil
	}

	return &e
}

// writeCache stores a measurement; a failure to write is logged, not fatal.
func writeCache(home, ip string, res *Result, now time.Time, log Logger) {
	if home == "" {
		return
	}

	path := filepath.Join(home, cacheFileName)
	data, err := json.MarshalIndent(cacheEntry{
		IP:         ip,
		Upload:     res.Upload,
		Download:   res.Download,
		MeasuredAt: now.UTC().Truncate(time.Second),
	}, "", "  ")
	if err == nil {
		err = os.WriteFile(path, append(data, '\n'), 0600)
	}
	if err != nil {
		log.Error("Could not store the bandwidth measurement", "path", path, "error", err)
		return
	}

	log.Debug("Stored the bandwidth measurement", "path", path)
}
