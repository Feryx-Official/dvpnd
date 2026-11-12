// SPDX-License-Identifier: Apache-2.0

package node

import (
	"encoding/base64"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	cmtlog "github.com/cometbft/cometbft/libs/log"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/Feryx-Official/dvpnd/v9/context"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// peerService holds peers under their session key with the counters the
// service reports, and records the ones the node removes.
type peerService struct {
	types.Service

	mu      sync.Mutex
	peers   map[string]types.Peer
	removed []string
}

func (s *peerService) Peers() ([]types.Peer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []types.Peer
	for _, p := range s.peers {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })

	return list, nil
}

func (s *peerService) HasPeer(data []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.peers[base64.StdEncoding.EncodeToString(data)]

	return ok
}

func (s *peerService) RemovePeer(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := base64.StdEncoding.EncodeToString(data)
	delete(s.peers, key)
	s.removed = append(s.removed, key)

	return nil
}

func (s *peerService) PeerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.peers)
}

func (s *peerService) wasRemoved(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.removed {
		if k == key {
			return true
		}
	}

	return false
}

// key is a session key: base64 of the peer data, as admission stores it.
func key(b byte) string { return base64.StdEncoding.EncodeToString([]byte{b, b, b, b}) }

// usageRig is a node with a database holding rows and a service holding
// peers with the given counters.
func usageRig(t *testing.T, rows []types.Session, peers ...types.Peer) (*Node, *peerService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "data.db")), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&types.Session{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := &peerService{peers: map[string]types.Peer{}}
	for _, p := range peers {
		service.peers[p.Key] = p
	}
	n := NewNode(context.NewContext().WithDatabase(db).WithLogger(cmtlog.NewNopLogger()).WithService(service))

	return n, service, db
}

func row(t *testing.T, db *gorm.DB, id uint64) types.Session {
	t.Helper()
	var item types.Session
	if err := db.Model(&types.Session{}).Where(&types.Session{ID: id}).First(&item).Error; err != nil {
		t.Fatal(err)
	}

	return item
}

// TestSetSessionsRemovesUnknownAndExhaustedPeers: a peer the node holds no
// session for is removed, so is a peer past what its session may still use;
// the usage of every known peer is stored first, and a peer within its
// allocation, or on a session without one, stays.
//
// Rules: [SL-9].
func TestSetSessionsRemovesUnknownAndExhaustedPeers(t *testing.T) {
	n, service, db := usageRig(t,
		[]types.Session{
			{ID: 1, Key: key(1), Address: "a", Available: 1000},
			{ID: 2, Key: key(2), Address: "b", Available: 100},
			{ID: 3, Key: key(3), Address: "c"}, // no cap
		},
		types.Peer{Key: key(1), Upload: 10, Download: 20},
		types.Peer{Key: key(2), Upload: 60, Download: 50},
		types.Peer{Key: key(3), Upload: 1 << 40, Download: 1 << 40},
		types.Peer{Key: key(9), Upload: 5},
	)

	if err := n.setSessions(); err != nil {
		t.Fatal(err)
	}

	if !service.wasRemoved(key(9)) {
		t.Error("a peer with no session row was left connected")
	}
	if !service.wasRemoved(key(2)) {
		t.Error("a peer past its allocation was left connected")
	}
	if service.wasRemoved(key(1)) || service.wasRemoved(key(3)) {
		t.Errorf("a peer within its allocation, or without one, was removed: %v", service.removed)
	}
	for id, want := range map[uint64][2]int64{1: {10, 20}, 2: {60, 50}, 3: {1 << 40, 1 << 40}} {
		if got := row(t, db, id); got.Upload != want[0] || got.Download != want[1] {
			t.Errorf("session %d stored upload %d download %d, want %d and %d", id, got.Upload, got.Download, want[0], want[1])
		}
	}
}

// TestSetSessionsStoresDownloadOnlyUsage: a pass that sees only the download
// grow still stores it and still checks the allocation.
//
// Rules: [SL-3].
func TestSetSessionsStoresDownloadOnlyUsage(t *testing.T) {
	n, service, db := usageRig(t,
		[]types.Session{
			{ID: 1, Key: key(1), Address: "a", Available: 1000, Upload: 10, Download: 20},
			{ID: 2, Key: key(2), Address: "b", Available: 100, Upload: 10, Download: 20},
		},
		types.Peer{Key: key(1), Upload: 10, Download: 500},
		types.Peer{Key: key(2), Upload: 10, Download: 500},
	)

	if err := n.setSessions(); err != nil {
		t.Fatal(err)
	}

	if got := row(t, db, 1); got.Download != 500 {
		t.Errorf("only the download moved: stored download %d, want 500", got.Download)
	}
	if !service.wasRemoved(key(2)) {
		t.Error("only the download moved, past the allocation: the peer was left connected")
	}
}

// TestSetSessionsCutsOffUsedHours: a peer served the hours its session paid
// for is removed by the usage pass, which stores its usage first; the time
// counts what the chain held when the peer was admitted. A peer with time
// left, or on a session without hours, stays.
//
// Rules: [SL-17].
func TestSetSessionsCutsOffUsedHours(t *testing.T) {
	since := func(d time.Duration) gorm.Model {
		at := time.Now().Add(-d)
		return gorm.Model{CreatedAt: at, UpdatedAt: at}
	}
	hour := int64(time.Hour)
	n, service, db := usageRig(t,
		[]types.Session{
			{ID: 1, Key: key(1), Address: "a", MaxDuration: hour, Model: since(61 * time.Minute)},
			{ID: 2, Key: key(2), Address: "b", MaxDuration: hour, Model: since(30 * time.Minute),
				BaseDuration: int64(31 * time.Minute)}, // served before a restart
			{ID: 3, Key: key(3), Address: "c", MaxDuration: 2 * hour, Model: since(61 * time.Minute)},
			{ID: 4, Key: key(4), Address: "d", Model: since(5 * time.Hour)}, // no hours
		},
		types.Peer{Key: key(1), Upload: 10, Download: 20},
		types.Peer{Key: key(2), Upload: 10, Download: 20},
		types.Peer{Key: key(3), Upload: 10, Download: 20},
		types.Peer{Key: key(4), Upload: 10, Download: 20},
	)

	if err := n.setSessions(); err != nil {
		t.Fatal(err)
	}

	if !service.wasRemoved(key(1)) {
		t.Error("a peer served past its paid hours was left connected")
	}
	if !service.wasRemoved(key(2)) {
		t.Error("a peer past its paid hours with what the chain held was left connected")
	}
	if service.wasRemoved(key(3)) || service.wasRemoved(key(4)) {
		t.Errorf("a peer with time left, or without hours, was removed: %v", service.removed)
	}
	if got := row(t, db, 1); got.Upload != 10 || got.Download != 20 {
		t.Errorf("the usage of the peer cut off was not stored: upload %d download %d", got.Upload, got.Download)
	}
}

// TestSetSessionsCutsOffIdlePeers: the checks that cut a peer off run whether
// or not it moved data since the last pass. A peer that idles past its paid
// hours is cut off then, not one pass after it sends again, and a peer past
// its allocation whose removal failed is removed again while it idles.
//
// Rules: [SL-16], [SL-17].
func TestSetSessionsCutsOffIdlePeers(t *testing.T) {
	at := time.Now().Add(-61 * time.Minute)
	since := gorm.Model{CreatedAt: at, UpdatedAt: at}
	n, service, db := usageRig(t,
		[]types.Session{
			{ID: 1, Key: key(1), Address: "a", MaxDuration: int64(time.Hour), Model: since, Upload: 10, Download: 20},
			{ID: 2, Key: key(2), Address: "b", Available: 25, Model: since, Upload: 10, Download: 20},
			{ID: 3, Key: key(3), Address: "c", MaxDuration: int64(2 * time.Hour), Available: 100, Model: since,
				Upload: 10, Download: 20},
		},
		types.Peer{Key: key(1), Upload: 10, Download: 20},
		types.Peer{Key: key(2), Upload: 10, Download: 20},
		types.Peer{Key: key(3), Upload: 10, Download: 20},
	)

	if err := n.setSessions(); err != nil {
		t.Fatal(err)
	}

	if !service.wasRemoved(key(1)) {
		t.Error("an idle peer past its paid hours was left connected")
	}
	if !service.wasRemoved(key(2)) {
		t.Error("an idle peer past its allocation was left connected")
	}
	if service.wasRemoved(key(3)) {
		t.Error("an idle peer with time and bytes left was removed")
	}
	if got := row(t, db, 1); !got.UpdatedAt.Equal(at) {
		t.Errorf("the row of a peer that moved nothing was written: updated_at %s", got.UpdatedAt)
	}
}

// lockedPeers is a peerService that records whether the admission lock was
// free when the counters were read.
type lockedPeers struct {
	*peerService
	lock   *sync.Mutex
	freeAt []bool
}

func (s *lockedPeers) Peers() ([]types.Peer, error) {
	free := s.lock.TryLock()
	if free {
		s.lock.Unlock()
	}
	s.freeAt = append(s.freeAt, free)

	return s.peerService.Peers()
}

// TestTheUsagePassReadsTheCountersUnderTheLock: a handshake stores what an
// evicted peer moved under the admission lock; a pass that read the counters
// before taking the lock would then write its older figures over them.
//
// Rules: [SL-18].
func TestTheUsagePassReadsTheCountersUnderTheLock(t *testing.T) {
	n, service, _ := usageRig(t,
		[]types.Session{{ID: 1, Key: key(1), Address: "a"}},
		types.Peer{Key: key(1), Upload: 10, Download: 20},
	)
	locked := &lockedPeers{peerService: service, lock: n.Admission()}
	n.WithService(locked)

	if err := n.setSessions(); err != nil {
		t.Fatal(err)
	}
	if len(locked.freeAt) != 1 || locked.freeAt[0] {
		t.Fatalf("the counters were read outside the admission lock: %v", locked.freeAt)
	}
}
