// SPDX-License-Identifier: Apache-2.0

package session

import (
	"crypto/rand"
	"encoding/base64"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	sdk "github.com/cosmos/cosmos-sdk/types"
	v1base "github.com/sentinel-official/sentinelhub/v12/types/v1"
	nodetypes "github.com/sentinel-official/sentinelhub/v12/x/node/types/v3"
	sessiontypes "github.com/sentinel-official/sentinelhub/v12/x/session/types/v3"
	v2subscriptiontypes "github.com/sentinel-official/sentinelhub/v12/x/subscription/types/v2"
	subscriptiontypes "github.com/sentinel-official/sentinelhub/v12/x/subscription/types/v3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/Feryx-Official/dvpnd/v9/context"
	"github.com/Feryx-Official/dvpnd/v9/lite"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// fakeService keeps peers in a map and takes a moment to add one, as a real
// service does, so concurrent handshakes overlap. counters holds what a
// connected peer has moved.
type fakeService struct {
	types.Service

	mu       sync.Mutex
	peers    map[string]bool
	counters map[string]types.Peer
}

func (f *fakeService) Peers() ([]types.Peer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var list []types.Peer
	for key := range f.peers {
		p := f.counters[key]
		p.Key = key
		list = append(list, p)
	}

	return list, nil
}

func (f *fakeService) AddPeer(data []byte) ([]byte, error) {
	time.Sleep(2 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.peers[base64.StdEncoding.EncodeToString(data)] = true

	return []byte("ok"), nil
}

func (f *fakeService) HasPeer(data []byte) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.peers[base64.StdEncoding.EncodeToString(data)]
}

func (f *fakeService) RemovePeer(data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.peers, base64.StdEncoding.EncodeToString(data))

	return nil
}

func (f *fakeService) PeerCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.peers)
}

// fakeChain answers every session as an active node session of this node
// for the account that asks, after a network-like delay.
type fakeChain struct {
	node    string
	account func(id uint64) string
}

func (f *fakeChain) QuerySession(id uint64) (sessiontypes.Session, error) {
	time.Sleep(time.Millisecond)

	return &nodetypes.Session{BaseSession: &sessiontypes.BaseSession{
		ID:            id,
		AccAddress:    f.account(id),
		NodeAddress:   f.node,
		DownloadBytes: sdkmath.ZeroInt(),
		UploadBytes:   sdkmath.ZeroInt(),
		MaxBytes:      sdkmath.ZeroInt(),
		Status:        v1base.StatusActive,
	}}, nil
}

func (f *fakeChain) QuerySubscription(uint64) (*subscriptiontypes.Subscription, error) {
	return nil, nil
}

func (f *fakeChain) QueryAllocation(uint64, sdk.AccAddress) (*v2subscriptiontypes.Allocation, error) {
	return nil, nil
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}

	return b
}

func admissionRig(t *testing.T, maxPeers int) (*context.Context, *fakeService, *gorm.DB, sdk.AccAddress) {
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

	config := types.NewConfig().WithDefaultValues()
	config.QOS.MaxPeers = maxPeers
	service := &fakeService{peers: map[string]bool{}, counters: map[string]types.Peer{}}
	ctx := context.NewContext().
		WithClient(lite.NewDefaultClient().WithFromAddress(randomBytes(t, 20))).
		WithConfig(config).
		WithDatabase(db).
		WithLogger(cmtlog.NewNopLogger()).
		WithService(service)

	return ctx, service, db, randomBytes(t, 20)
}

func rows(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&types.Session{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}

	return n
}

// TestConcurrentHandshakesHoldMaxPeers: many clients at once cannot together
// exceed max_peers, and every admitted peer has its row.
//
// Rules: [SL-4].
func TestConcurrentHandshakesHoldMaxPeers(t *testing.T) {
	ctx, service, db, _ := admissionRig(t, 3)
	accounts := map[uint64]sdk.AccAddress{}
	for id := uint64(1); id <= 20; id++ {
		accounts[id] = randomBytes(t, 20)
	}
	chain := &fakeChain{node: ctx.Address().String(), account: func(id uint64) string { return accounts[id].String() }}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		admitted int
	)
	for id := uint64(1); id <= 20; id++ {
		wg.Add(1)
		go func(id uint64) {
			defer wg.Done()
			_, apiErr := admit(ctx, chain, admitRequest{AccAddress: accounts[id], ID: id, PeerData: randomBytes(t, 32)})
			if apiErr == nil {
				mu.Lock()
				admitted++
				mu.Unlock()
			} else if apiErr.Code != 1 {
				t.Errorf("session %d: %v", id, apiErr)
			}
		}(id)
	}
	wg.Wait()

	if admitted != 3 || service.PeerCount() != 3 || rows(t, db) != 3 {
		t.Fatalf("admitted %d, peers %d, rows %d; want 3 each", admitted, service.PeerCount(), rows(t, db))
	}
}

// TestConcurrentHandshakesForOneSession: the same session handshaking many
// times at once (a retrying client, or a replay) gets one peer and one row.
//
// Rules: [SL-4].
func TestConcurrentHandshakesForOneSession(t *testing.T) {
	ctx, service, db, account := admissionRig(t, 50)
	chain := &fakeChain{node: ctx.Address().String(), account: func(uint64) string { return account.String() }}

	var wg sync.WaitGroup
	results := make(chan *apiError, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, apiErr := admit(ctx, chain, admitRequest{AccAddress: account, ID: 42, PeerData: randomBytes(t, 32)})
			results <- apiErr
		}()
	}
	wg.Wait()
	close(results)

	ok := 0
	for apiErr := range results {
		switch {
		case apiErr == nil:
			ok++
		case apiErr.Status != 409:
			t.Errorf("unexpected refusal: %v", apiErr)
		}
	}
	if ok != 1 || service.PeerCount() != 1 || rows(t, db) != 1 {
		t.Fatalf("admitted %d, peers %d, rows %d; want 1 each", ok, service.PeerCount(), rows(t, db))
	}
}

// TestFailedRecordRemovesThePeer: when the session row cannot be written the
// peer comes out again, so it is never served unmetered.
//
// Rules: [SL-5].
func TestFailedRecordRemovesThePeer(t *testing.T) {
	ctx, service, db, account := admissionRig(t, 50)
	chain := &fakeChain{node: ctx.Address().String(), account: func(uint64) string { return account.String() }}

	// A soft-deleted row is invisible to the duplicate checks but still
	// holds the primary key, so the insert fails.
	db.Create(&types.Session{ID: 7, Key: "old", Address: "old"})
	db.Delete(&types.Session{}, 7)

	_, apiErr := admit(ctx, chain, admitRequest{AccAddress: account, ID: 7, PeerData: randomBytes(t, 32)})
	if apiErr == nil || apiErr.Status != 500 {
		t.Fatalf("admit: %v", apiErr)
	}
	if service.PeerCount() != 0 {
		t.Fatalf("%d peers left without a row", service.PeerCount())
	}
}

// Rules: [SL-6].
func TestAdmissionRefusesAnotherNodesSession(t *testing.T) {
	ctx, service, _, account := admissionRig(t, 50)
	chain := &fakeChain{node: "sentnode1someoneelse", account: func(uint64) string { return account.String() }}

	_, apiErr := admit(ctx, chain, admitRequest{AccAddress: account, ID: 1, PeerData: randomBytes(t, 32)})
	if apiErr == nil || apiErr.Code != 7 || service.PeerCount() != 0 {
		t.Fatalf("admit: %v, peers %d", apiErr, service.PeerCount())
	}
}
