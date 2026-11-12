// SPDX-License-Identifier: Apache-2.0

package session

import (
	"encoding/base64"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	v1base "github.com/sentinel-official/sentinelhub/v12/types/v1"
	nodetypes "github.com/sentinel-official/sentinelhub/v12/x/node/types/v3"
	sessiontypes "github.com/sentinel-official/sentinelhub/v12/x/session/types/v3"
	v2subscriptiontypes "github.com/sentinel-official/sentinelhub/v12/x/subscription/types/v2"
	subscriptiontypes "github.com/sentinel-official/sentinelhub/v12/x/subscription/types/v3"

	"github.com/Feryx-Official/dvpnd/v9/types"
)

// scriptChain answers with what a test sets: nil is "no such object".
type scriptChain struct {
	session      sessiontypes.Session
	subscription *subscriptiontypes.Subscription
	allocation   *v2subscriptiontypes.Allocation
}

func (c *scriptChain) QuerySession(uint64) (sessiontypes.Session, error) { return c.session, nil }
func (c *scriptChain) QuerySubscription(uint64) (*subscriptiontypes.Subscription, error) {
	return c.subscription, nil
}
func (c *scriptChain) QueryAllocation(uint64, sdk.AccAddress) (*v2subscriptiontypes.Allocation, error) {
	return c.allocation, nil
}

// baseSession is an active session with nothing used and no byte cap.
func baseSession(id uint64, account, node string) *sessiontypes.BaseSession {
	return &sessiontypes.BaseSession{
		ID: id, AccAddress: account, NodeAddress: node, Status: v1base.StatusActive,
		DownloadBytes: sdkmath.ZeroInt(), UploadBytes: sdkmath.ZeroInt(), MaxBytes: sdkmath.ZeroInt(),
	}
}

func nodeSession(b *sessiontypes.BaseSession) sessiontypes.Session {
	return &nodetypes.Session{BaseSession: b}
}

func planSession(b *sessiontypes.BaseSession, subscription uint64) sessiontypes.Session {
	return &subscriptiontypes.Session{BaseSession: b, SubscriptionID: subscription}
}

func allocation(granted, utilised int64) *v2subscriptiontypes.Allocation {
	return &v2subscriptiontypes.Allocation{GrantedBytes: sdkmath.NewInt(granted), UtilisedBytes: sdkmath.NewInt(utilised)}
}

// TestAdmissionRefusals: a session that does not exist, is not active, is
// another account's or another node's, or a plan session without an active
// subscription and an allocation, gets no peer and no row. The same plan
// session is admitted once the allocation is there.
//
// Rules: [SL-6].
func TestAdmissionRefusals(t *testing.T) {
	ctx, service, db, account := admissionRig(t, 50)
	node, acct := ctx.Address().String(), account.String()
	other := sdk.AccAddress(randomBytes(t, 20)).String()
	active := &subscriptiontypes.Subscription{ID: 5, Status: v1base.StatusActive}

	inactive := baseSession(1, acct, node)
	inactive.Status = v1base.StatusInactivePending

	for _, c := range []struct {
		name         string
		chain        *scriptChain
		status, code int
	}{
		{"no such session", &scriptChain{}, 404, 5},
		{"inactive session", &scriptChain{session: nodeSession(inactive)}, 404, 5},
		{"another account's session", &scriptChain{session: nodeSession(baseSession(1, other, node))}, 400, 5},
		{"another node's session", &scriptChain{session: nodeSession(baseSession(1, acct, other))}, 400, 7},
		{"plan session without its subscription", &scriptChain{session: planSession(baseSession(1, acct, node), 5)}, 404, 6},
		{"plan session, subscription not active", &scriptChain{session: planSession(baseSession(1, acct, node), 5),
			subscription: &subscriptiontypes.Subscription{ID: 5, Status: v1base.StatusInactivePending}}, 400, 6},
		{"plan session without an allocation", &scriptChain{session: planSession(baseSession(1, acct, node), 5),
			subscription: active}, 404, 8},
	} {
		_, apiErr := admit(ctx, c.chain, admitRequest{AccAddress: account, ID: 1, PeerData: randomBytes(t, 32)})
		if apiErr == nil || apiErr.Status != c.status || apiErr.Code != c.code {
			t.Errorf("%s: got %v, want status %d code %d", c.name, apiErr, c.status, c.code)
		}
		if service.PeerCount() != 0 || rows(t, db) != 0 {
			t.Fatalf("%s: refused, yet %d peers and %d rows", c.name, service.PeerCount(), rows(t, db))
		}
	}

	_, apiErr := admit(ctx, &scriptChain{session: planSession(baseSession(1, acct, node), 5), subscription: active,
		allocation: allocation(1000, 0)}, admitRequest{AccAddress: account, ID: 1, PeerData: randomBytes(t, 32)})
	if apiErr != nil || service.PeerCount() != 1 || rows(t, db) != 1 {
		t.Fatalf("a plan session with room in its allocation: %v, %d peers, %d rows", apiErr, service.PeerCount(), rows(t, db))
	}
}

// TestAdmissionEnforcesByteCaps: a session that has used its max_bytes, or a
// plan allocation used up counting what this node served but has not
// reported, is refused; otherwise the peer may use what is left of the
// smaller of the two.
//
// Rules: [SL-7].
func TestAdmissionEnforcesByteCaps(t *testing.T) {
	type served struct{ download int64 } // an earlier session of the account on this node
	for _, c := range []struct {
		name          string
		max, used     int64 // the session's max_bytes and what the chain says it used
		alloc         *v2subscriptiontypes.Allocation
		earlier       *served
		wantAvailable int64 // -1: refused
	}{
		{"node session at its max_bytes", 1000, 1000, nil, nil, -1},
		{"node session below its max_bytes", 1000, 400, nil, nil, 600},
		{"node session without a cap", 0, 5000, nil, nil, 0},
		{"allocation used up with what this node served", 0, 0, allocation(1000, 600), &served{400}, -1},
		{"allocation with room left after what this node served", 0, 0, allocation(1000, 600), &served{300}, 100},
		{"the allocation leaves less than max_bytes", 1000, 500, allocation(1000, 900), nil, 100},
		{"max_bytes leaves less than the allocation", 1000, 950, allocation(1000, 0), nil, 50},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, service, db, account := admissionRig(t, 50)
			b := baseSession(1, account.String(), ctx.Address().String())
			b.MaxBytes, b.DownloadBytes = sdkmath.NewInt(c.max), sdkmath.NewInt(c.used)
			chain := &scriptChain{session: nodeSession(b)}
			if c.alloc != nil {
				chain = &scriptChain{session: planSession(b, 5), allocation: c.alloc,
					subscription: &subscriptiontypes.Subscription{ID: 5, Status: v1base.StatusActive}}
			}
			if c.earlier != nil {
				db.Create(&types.Session{ID: 2, Subscription: 5, Address: account.String(),
					Key: base64.StdEncoding.EncodeToString(randomBytes(t, 32)), Download: c.earlier.download})
			}

			_, apiErr := admit(ctx, chain, admitRequest{AccAddress: account, ID: 1, PeerData: randomBytes(t, 32)})
			if c.wantAvailable < 0 {
				if apiErr == nil || apiErr.Code != 8 || service.PeerCount() != 0 {
					t.Fatalf("got %v with %d peers, want a refusal with code 8 and no peer", apiErr, service.PeerCount())
				}
				return
			}
			if apiErr != nil {
				t.Fatalf("refused: %v", apiErr)
			}
			var item types.Session
			db.Model(&types.Session{}).Where(&types.Session{ID: 1}).First(&item)
			if item.Available != c.wantAvailable {
				t.Fatalf("the peer may use %d bytes, want %d", item.Available, c.wantAvailable)
			}
		})
	}
}

// TestAdmissionEnforcesPaidTime: an hourly session that has used the hours
// it paid for is refused, the way a session that has used its max_bytes is;
// one with time left, or a session without hours (a gigabyte session), is
// admitted and its hours are recorded for the usage pass. The chain pays an hourly session for the duration the node
// reports, up to what the client deposited for its hours, so time served
// beyond them is never paid.
//
// Rules: [SL-17].
func TestAdmissionEnforcesPaidTime(t *testing.T) {
	for _, c := range []struct {
		name      string
		max, used time.Duration // the session's max_duration and the duration the chain holds
		refused   bool
	}{
		{"hourly session at its paid time", time.Hour, time.Hour, true},
		{"hourly session past its paid time", time.Hour, time.Hour + 5*time.Minute, true},
		{"hourly session with time left", 2 * time.Hour, 30 * time.Minute, false},
		{"session without hours", 0, 5 * time.Hour, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, service, db, account := admissionRig(t, 50)
			b := baseSession(1, account.String(), ctx.Address().String())
			b.MaxDuration, b.Duration = c.max, c.used

			_, apiErr := admit(ctx, &scriptChain{session: nodeSession(b)},
				admitRequest{AccAddress: account, ID: 1, PeerData: randomBytes(t, 32)})
			if c.refused {
				if apiErr == nil || apiErr.Code != 8 || service.PeerCount() != 0 {
					t.Fatalf("got %v with %d peers, want a refusal with code 8 and no peer", apiErr, service.PeerCount())
				}
				return
			}
			if apiErr != nil || service.PeerCount() != 1 {
				t.Fatalf("got %v with %d peers, want the peer admitted", apiErr, service.PeerCount())
			}
			var item types.Session
			db.Model(&types.Session{}).Where(&types.Session{ID: 1}).First(&item)
			if time.Duration(item.MaxDuration) != c.max {
				t.Fatalf("recorded paid hours %s, want %s", time.Duration(item.MaxDuration), c.max)
			}
		})
	}
}

// TestAnAccountKeepsOnePeer: a new handshake from an account evicts its
// earlier peer; another account's peer stays.
//
// Rules: [SL-8].
func TestAnAccountKeepsOnePeer(t *testing.T) {
	ctx, service, _, account := admissionRig(t, 50)
	other := sdk.AccAddress(randomBytes(t, 20))
	chain := &fakeChain{node: ctx.Address().String(), account: func(id uint64) string {
		if id == 3 {
			return other.String()
		}
		return account.String()
	}}
	first, second, others := randomBytes(t, 32), randomBytes(t, 32), randomBytes(t, 32)

	for _, r := range []admitRequest{
		{AccAddress: account, ID: 1, PeerData: first},
		{AccAddress: other, ID: 3, PeerData: others},
		{AccAddress: account, ID: 2, PeerData: second},
	} {
		if _, apiErr := admit(ctx, chain, r); apiErr != nil {
			t.Fatalf("session %d: %v", r.ID, apiErr)
		}
	}

	if service.HasPeer(first) {
		t.Error("the account's earlier peer is still connected")
	}
	if !service.HasPeer(second) || !service.HasPeer(others) {
		t.Error("the new peer, or another account's, is gone")
	}
}

// sessionsChain answers each session id with its own session; subscriptions
// are active and allocations hold room.
type sessionsChain struct {
	sessions map[uint64]sessiontypes.Session
}

func (c *sessionsChain) QuerySession(id uint64) (sessiontypes.Session, error) {
	return c.sessions[id], nil
}
func (c *sessionsChain) QuerySubscription(id uint64) (*subscriptiontypes.Subscription, error) {
	return &subscriptiontypes.Subscription{ID: id, Status: v1base.StatusActive}, nil
}
func (c *sessionsChain) QueryAllocation(uint64, sdk.AccAddress) (*v2subscriptiontypes.Allocation, error) {
	return allocation(1<<40, 0), nil
}

// TestAnAccountKeepsOnePeerAcrossItsSessions: the account's earlier peer is
// evicted whatever session it served, a node session or a plan session on
// this subscription or another.
//
// Rules: [SL-8].
func TestAnAccountKeepsOnePeerAcrossItsSessions(t *testing.T) {
	ctx, service, _, account := admissionRig(t, 50)
	node, acct := ctx.Address().String(), account.String()
	chain := &sessionsChain{sessions: map[uint64]sessiontypes.Session{
		1: nodeSession(baseSession(1, acct, node)),
		2: planSession(baseSession(2, acct, node), 5),
		3: planSession(baseSession(3, acct, node), 7),
		4: planSession(baseSession(4, acct, node), 7),
		5: nodeSession(baseSession(5, acct, node)),
	}}

	var peers [][]byte
	for id := uint64(1); id <= 5; id++ {
		data := randomBytes(t, 32)
		if _, apiErr := admit(ctx, chain, admitRequest{AccAddress: account, ID: id, PeerData: data}); apiErr != nil {
			t.Fatalf("session %d: %v", id, apiErr)
		}
		peers = append(peers, data)
		for i, p := range peers {
			if connected, latest := service.HasPeer(p), i == len(peers)-1; connected != latest {
				t.Fatalf("after session %d: the peer of session %d connected %v", id, i+1, connected)
			}
		}
	}
}

// TestAnEvictedPeersUsageIsKept: what an account's earlier peer moved since
// the last pass is stored before a new handshake evicts it, so the next pass
// reports it; on a plan session it counts against the allocation the new
// handshake asks for.
//
// Rules: [SL-7], [SL-18].
func TestAnEvictedPeersUsageIsKept(t *testing.T) {
	ctx, service, db, account := admissionRig(t, 50)
	node, acct := ctx.Address().String(), account.String()
	chain := &sessionsChain{sessions: map[uint64]sessiontypes.Session{
		1: nodeSession(baseSession(1, acct, node)),
		2: nodeSession(baseSession(2, acct, node)),
	}}

	first := randomBytes(t, 32)
	if _, apiErr := admit(ctx, chain, admitRequest{AccAddress: account, ID: 1, PeerData: first}); apiErr != nil {
		t.Fatal(apiErr)
	}
	service.mu.Lock()
	service.counters[base64.StdEncoding.EncodeToString(first)] = types.Peer{Upload: 10, Download: 20}
	service.mu.Unlock()
	if _, apiErr := admit(ctx, chain, admitRequest{AccAddress: account, ID: 2, PeerData: randomBytes(t, 32)}); apiErr != nil {
		t.Fatal(apiErr)
	}
	if service.HasPeer(first) {
		t.Fatal("the earlier peer was not evicted")
	}
	var item types.Session
	if err := db.Where(&types.Session{ID: 1}).First(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.Upload != 10 || item.Download != 20 {
		t.Fatalf("the evicted peer's usage was not kept: upload %d download %d", item.Upload, item.Download)
	}

	// A plan session: the earlier peer has used the allocation since the
	// last pass, so the next handshake on it is refused.
	ctx, service, _, account = admissionRig(t, 50)
	node, acct = ctx.Address().String(), account.String()
	plan := &scriptChain{
		subscription: &subscriptiontypes.Subscription{ID: 5, Status: v1base.StatusActive},
		allocation:   allocation(100, 0),
	}
	plan.session = planSession(baseSession(3, acct, node), 5)
	earlier := randomBytes(t, 32)
	if _, apiErr := admit(ctx, plan, admitRequest{AccAddress: account, ID: 3, PeerData: earlier}); apiErr != nil {
		t.Fatal(apiErr)
	}
	service.mu.Lock()
	service.counters[base64.StdEncoding.EncodeToString(earlier)] = types.Peer{Upload: 60, Download: 50}
	service.mu.Unlock()
	plan.session = planSession(baseSession(4, acct, node), 5)
	if _, apiErr := admit(ctx, plan, admitRequest{AccAddress: account, ID: 4, PeerData: randomBytes(t, 32)}); apiErr == nil || apiErr.Code != 8 {
		t.Fatalf("a handshake on a used allocation was admitted: %v", apiErr)
	}
}
