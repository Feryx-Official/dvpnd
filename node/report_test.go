// SPDX-License-Identifier: Apache-2.0

package node

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"
	v1base "github.com/Feryx-Official/feryxhub/v12/types/v1"
	nodetypes "github.com/Feryx-Official/feryxhub/v12/x/node/types/v3"
	sessiontypes "github.com/Feryx-Official/feryxhub/v12/x/session/types/v3"
	subscriptiontypes "github.com/Feryx-Official/feryxhub/v12/x/subscription/types/v3"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	"gorm.io/gorm"

	"github.com/Feryx-Official/dvpnd/v9/context"
	"github.com/Feryx-Official/dvpnd/v9/lite"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// fakeChain holds sessions by id and refuses a report that names a session
// it does not hold as active, failing the whole transaction the way the
// chain does.
type fakeChain struct {
	mu       sync.Mutex
	sessions map[uint64]v1base.Status
	down     bool       // every call fails, as in an RPC outage
	sent     [][]uint64 // the ids of every report attempted
	accepted []uint64
}

func (f *fakeChain) QuerySession(id uint64) (sessiontypes.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("connection refused")
	}
	status, ok := f.sessions[id]
	if !ok {
		return nil, nil
	}

	return &nodetypes.Session{BaseSession: &sessiontypes.BaseSession{
		ID: id, Status: status,
		DownloadBytes: sdkmath.ZeroInt(), UploadBytes: sdkmath.ZeroInt(), MaxBytes: sdkmath.ZeroInt(),
	}}, nil
}

func (f *fakeChain) QuerySubscription(uint64) (*subscriptiontypes.Subscription, error) {
	return nil, nil
}

func (f *fakeChain) UpdateSessions(items ...types.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []uint64
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	f.sent = append(f.sent, ids)
	if f.down {
		return errors.New("connection refused")
	}
	for _, id := range ids {
		if status, ok := f.sessions[id]; !ok || status.Equal(v1base.StatusInactive) {
			return errors.New("session not found")
		}
	}
	f.accepted = append(f.accepted, ids...)

	return nil
}

func testNode(chain *fakeChain) *Node {
	n := NewNode(context.NewContext().WithLogger(cmtlog.NewNopLogger()))
	n.chain = chain

	return n
}

func sessions(ids ...uint64) []types.Session {
	var items []types.Session
	for _, id := range ids {
		items = append(items, types.Session{ID: id, Key: "k"})
	}

	return items
}

// TestReportDropsASessionTheChainEnded: one session the chain deleted between
// the query and the send no longer holds back the others' reports.
//
// Rules: [SL-11].
func TestReportDropsASessionTheChainEnded(t *testing.T) {
	chain := &fakeChain{sessions: map[uint64]v1base.Status{
		1: v1base.StatusActive, 3: v1base.StatusInactivePending,
	}}

	if err := testNode(chain).reportSessions(sessions(1, 2, 3)); err != nil {
		t.Fatal(err)
	}
	if got := chain.accepted; len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("accepted %v, want [1 3]", got)
	}
	if len(chain.sent) != 2 {
		t.Fatalf("sent %v: want the batch, then the batch without session 2", chain.sent)
	}
}

// TestReportOneAtATime: when the second batch still fails, each session is
// sent on its own and the good ones get through.
//
// Rules: [SL-11].
func TestReportOneAtATime(t *testing.T) {
	chain := &fakeChain{sessions: map[uint64]v1base.Status{1: v1base.StatusActive, 2: v1base.StatusActive}}
	n := testNode(chain)
	// Session 2 is reported active by the query but refused by the chain,
	// as if it ended between the two.
	n.chain = &refusing{fakeChain: chain, refuse: 2}

	err := n.reportSessions(sessions(1, 2))
	if err == nil || !strings.Contains(err.Error(), "session 2") {
		t.Fatalf("err = %v", err)
	}
	if got := chain.accepted; len(got) != 1 || got[0] != 1 {
		t.Fatalf("accepted %v, want [1]", got)
	}
}

type refusing struct {
	*fakeChain
	refuse uint64
}

func (r *refusing) UpdateSessions(items ...types.Session) error {
	for _, item := range items {
		if item.ID == r.refuse {
			r.mu.Lock()
			r.sent = append(r.sent, []uint64{item.ID})
			r.mu.Unlock()
			return errors.New("session not found")
		}
	}

	return r.fakeChain.UpdateSessions(items...)
}

// TestReportWaitsForABatchThatMayStillLand: a batch a remote took but no
// block included in time may still land. Sending its sessions again, alone
// or in a smaller batch, would pay for their reports twice, so the report
// waits for the next pass, which sees what the chain then holds.
//
// Rules: [CH-11].
func TestReportWaitsForABatchThatMayStillLand(t *testing.T) {
	chain := &fakeChain{sessions: map[uint64]v1base.Status{1: v1base.StatusActive, 2: v1base.StatusActive}}
	n := testNode(chain)
	n.chain = &unconfirmed{fakeChain: chain}

	if err := n.reportSessions(sessions(1, 2)); !errors.Is(err, lite.ErrUnconfirmed) {
		t.Fatalf("err = %v, want the batch's ErrUnconfirmed", err)
	}
	if len(chain.sent) != 1 {
		t.Fatalf("sent %v: the batch only, then wait for the next pass", chain.sent)
	}
}

// unconfirmed takes every report and never sees it in a block.
type unconfirmed struct{ *fakeChain }

func (u *unconfirmed) UpdateSessions(items ...types.Session) error {
	var ids []uint64
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	u.mu.Lock()
	u.sent = append(u.sent, ids)
	u.mu.Unlock()

	return fmt.Errorf("https://rpc.example:443: %w: after 30 seconds", lite.ErrUnconfirmed)
}

// TestChainBytesPastInt64DoNotStopTheNode: the chain holds a session's bytes
// as an integer of any size. A value past int64 is compared, not converted,
// so the pass goes on instead of the job panicking, which stops the node.
//
// Rules: [SL-12].
func TestChainBytesPastInt64DoNotStopTheNode(t *testing.T) {
	huge := sdkmath.NewIntFromUint64(math.MaxUint64)
	session := &nodetypes.Session{BaseSession: &sessiontypes.BaseSession{
		ID: 1, Status: v1base.StatusActive,
		DownloadBytes: huge, UploadBytes: huge, MaxBytes: sdkmath.ZeroInt(),
	}}
	n := testNode(&fakeChain{})
	n.chain = &holding{session: session}
	item := types.Session{ID: 1, Key: "k", Upload: 10, Download: 20}

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("checking the session panicked: %v", r)
			}
		}()
		if _, _, skip, err := n.checkSession(item); err != nil || skip {
			t.Fatalf("skip %v, err %v: usage that differs from the chain's is reported", skip, err)
		}
		if !sessionNeedsReport(item, session) {
			t.Fatal("usage that differs from the chain's needs a final report")
		}
	}()
}

// TestAnIdleSessionLogsNothingAtInfo: a session that moved no data is seen
// on every pass; at the default log level that adds nothing to the journal.
func TestAnIdleSessionLogsNothingAtInfo(t *testing.T) {
	var out bytes.Buffer
	n := NewNode(context.NewContext().WithLogger(cmtlog.NewFilter(cmtlog.NewTMLogger(&out), cmtlog.AllowInfo())))
	n.chain = &holding{session: &nodetypes.Session{BaseSession: &sessiontypes.BaseSession{
		ID: 1, Status: v1base.StatusActive,
		DownloadBytes: sdkmath.NewInt(20), UploadBytes: sdkmath.NewInt(10), MaxBytes: sdkmath.ZeroInt(),
	}}}

	_, _, skip, err := n.checkSession(types.Session{ID: 1, Key: "k", Upload: 10, Download: 20})
	if err != nil || !skip {
		t.Fatalf("skip %v, err %v: an idle session is not reported", skip, err)
	}
	if out.Len() > 0 {
		t.Fatalf("an idle session logged at info:\n%s", out.String())
	}
}

// holding holds one session.
type holding struct {
	fakeChain
	session sessiontypes.Session
}

func (h *holding) QuerySession(uint64) (sessiontypes.Session, error) { return h.session, nil }

// TestReportDuringAnOutage: with the chain unreachable the report is left
// for the next pass, not sent session by session.
//
// Rules: [SL-11].
func TestReportDuringAnOutage(t *testing.T) {
	chain := &fakeChain{down: true, sessions: map[uint64]v1base.Status{}}

	if err := testNode(chain).reportSessions(sessions(1, 2, 3)); err == nil {
		t.Fatal("an outage must be reported")
	}
	if len(chain.sent) != 1 {
		t.Fatalf("sent %v: one attempt, then wait for the next pass", chain.sent)
	}
}

// TestJobPanicEndsTheNodeCleanly: a job that panics hands an error to the
// node's error channel instead of crashing the process, so the caller still
// stops the VPN service.
//
// Rules: [SL-12].
func TestJobPanicEndsTheNodeCleanly(t *testing.T) {
	n := testNode(&fakeChain{})
	errCh := make(chan error, 1)

	go n.runJob("boom", time.Hour, func() error { panic("nil map") }, errCh)

	select {
	case err := <-errCh:
		if !strings.Contains(err.Error(), "job boom panicked: nil map") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no error from a panicking job")
	}
}

// TestAFailedPassIsTriedAgain: a pass that fails is logged and run again at
// the next tick; the job carries on rather than ending, and so does the node.
//
// Rules: [SL-12].
func TestAFailedPassIsTriedAgain(t *testing.T) {
	n := testNode(&fakeChain{})
	errCh := make(chan error, 1)
	var passes atomic.Int32
	pass := func() error {
		if passes.Add(1) < 3 {
			return errors.New("connection refused")
		}
		panic("third pass") // a panic is the one way out of a job, so the test can end it
	}

	go n.runJob("retry", time.Millisecond, pass, errCh)

	select {
	case err := <-errCh:
		if !strings.Contains(err.Error(), "job retry panicked: third pass") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the job stopped after %d passes; a failed pass must be tried again", passes.Load())
	}
}

// TestAJobRunsItsFirstPassAtOnce: the first pass does not wait for a tick,
// because a freshly registered node is inactive until its first status
// update.
//
// Rules: [SL-13].
func TestAJobRunsItsFirstPassAtOnce(t *testing.T) {
	n := testNode(&fakeChain{})
	errCh := make(chan error, 1)

	go n.runJob("first", time.Hour, func() error { panic("first pass") }, errCh)

	select {
	case err := <-errCh:
		if !strings.Contains(err.Error(), "first pass") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first pass waited for a tick")
	}
}

// peerSet is a VPN service that only keeps a set of peers.
type peerSet struct {
	types.Service
	peers map[string]bool
}

func (p *peerSet) HasPeer(data []byte) bool { return p.peers[string(data)] }
func (p *peerSet) RemovePeer(data []byte) error {
	delete(p.peers, string(data))
	return nil
}

// TestUpdateSessionsPass: a session the chain dropped loses its peer and its
// row, a session the chain cannot be asked about waits for the next pass, and
// the rest is reported, all in one pass that does not stop at the first
// problem.
//
// Rules: [SL-10].
func TestUpdateSessionsPass(t *testing.T) {
	db := testDB(t)
	// Keys are base64 of the peer data the service holds.
	db.Create(&types.Session{ID: 1, Key: "YQ==", Address: "a", Upload: 10, Download: 10}) // "a"
	db.Create(&types.Session{ID: 2, Key: "Yg==", Address: "b", Upload: 10, Download: 10}) // "b"
	db.Create(&types.Session{ID: 3, Key: "Yw==", Address: "c", Upload: 10, Download: 10}) // "c"

	service := &peerSet{peers: map[string]bool{"a": true, "b": true, "c": true}}
	chain := &fakeChain{sessions: map[uint64]v1base.Status{1: v1base.StatusActive}}
	n := NewNode(context.NewContext().WithLogger(cmtlog.NewNopLogger()).WithDatabase(db).WithService(service))
	n.chain = &flaky{fakeChain: chain, fail: 3}

	err := n.updateSessions()
	if err == nil || !strings.Contains(err.Error(), "session 3") {
		t.Fatalf("err = %v", err)
	}
	if got := chain.accepted; len(got) != 1 || got[0] != 1 {
		t.Fatalf("reported %v, want [1]", got)
	}
	if service.peers["b"] || !service.peers["a"] || !service.peers["c"] {
		t.Fatalf("peers %v: only the dropped session's peer goes", service.peers)
	}
	var ids []uint64
	db.Model(&types.Session{}).Order("id").Pluck("id", &ids)
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 3 {
		t.Fatalf("rows %v, want [1 3]", ids)
	}
}

// TestUpdateSessionsCutsOffUsedHours: the pass that asks the chain about each
// session removes the peer of an hourly session that has used the hours it
// paid for, and still reports its usage, which is what the node is paid on; a
// peer with time left, or on a session without hours, stays.
//
// Rules: [SL-17].
func TestUpdateSessionsCutsOffUsedHours(t *testing.T) {
	db := testDB(t)
	start := time.Now().Add(-2 * time.Hour)
	served := func(id uint64, key string, d time.Duration) {
		db.Create(&types.Session{ID: id, Key: key, Address: key, Upload: 10, Download: 10,
			CreatedAt: start, UpdatedAt: start.Add(d)})
	}
	served(1, "YQ==", 61*time.Minute) // "a": one hour paid
	served(2, "Yg==", 30*time.Minute) // "b": two hours paid
	served(3, "Yw==", 61*time.Minute) // "c": no hours, a gigabyte session

	service := &peerSet{peers: map[string]bool{"a": true, "b": true, "c": true}}
	chain := &fakeChain{sessions: map[uint64]v1base.Status{
		1: v1base.StatusActive, 2: v1base.StatusActive, 3: v1base.StatusActive}}
	n := NewNode(context.NewContext().WithLogger(cmtlog.NewNopLogger()).WithDatabase(db).WithService(service))
	n.chain = &timedChain{fakeChain: chain, hours: map[uint64]time.Duration{1: time.Hour, 2: 2 * time.Hour}}

	if err := n.updateSessions(); err != nil {
		t.Fatal(err)
	}
	if service.peers["a"] {
		t.Error("the peer of an hourly session past its paid hours was left connected")
	}
	if !service.peers["b"] || !service.peers["c"] {
		t.Errorf("peers %v: a peer with time left, or without hours, was removed", service.peers)
	}
	if got := chain.accepted; len(got) != 3 {
		t.Errorf("reported %v, want all three: the usage of a session cut off is still paid", got)
	}
}

// timedChain is fakeChain with hours: a session it lists carries that
// max_duration.
type timedChain struct {
	*fakeChain
	hours map[uint64]time.Duration
}

func (c *timedChain) QuerySession(id uint64) (sessiontypes.Session, error) {
	s, err := c.fakeChain.QuerySession(id)
	if s != nil {
		s.(*nodetypes.Session).MaxDuration = c.hours[id]
	}

	return s, err
}

// flaky fails the session query for one id.
type flaky struct {
	*fakeChain
	fail uint64
}

func (f *flaky) QuerySession(id uint64) (sessiontypes.Session, error) {
	if id == f.fail {
		return nil, errors.New("timeout")
	}

	return f.fakeChain.QuerySession(id)
}

func testDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := OpenDatabase(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}

	return db
}
