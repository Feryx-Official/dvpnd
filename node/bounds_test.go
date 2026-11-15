// SPDX-License-Identifier: Apache-2.0

package node

import (
	"errors"
	"math"
	"math/rand"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"
	base "github.com/Feryx-Official/feryxhub/v12/types"
	v1base "github.com/Feryx-Official/feryxhub/v12/types/v1"
	nodetypes "github.com/Feryx-Official/feryxhub/v12/x/node/types/v3"
	sessiontypes "github.com/Feryx-Official/feryxhub/v12/x/session/types/v3"
	v2subscriptiontypes "github.com/Feryx-Official/feryxhub/v12/x/subscription/types/v2"
	subscriptiontypes "github.com/Feryx-Official/feryxhub/v12/x/subscription/types/v3"
	cmtlog "github.com/cometbft/cometbft/libs/log"

	"github.com/Feryx-Official/dvpnd/v9/context"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

var blockTime = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func at(t time.Time) func() (time.Time, error) {
	return func() (time.Time, error) { return t, nil }
}

// storedRow is a stored session whose totals are download, upload and duration.
func storedRow(id uint64, download, upload int64, duration time.Duration) types.Session {
	return types.Session{ID: id, Key: key(byte(id)), Download: download, Upload: upload,
		BaseDuration: int64(duration), CreatedAt: blockTime, UpdatedAt: blockTime}
}

func byteSession(id uint64, maxBytes, download, upload int64) *nodetypes.Session {
	return &nodetypes.Session{BaseSession: &sessiontypes.BaseSession{
		ID: id, Status: v1base.StatusActive, StartAt: blockTime.Add(-time.Hour),
		DownloadBytes: sdkmath.NewInt(download), UploadBytes: sdkmath.NewInt(upload),
		MaxBytes: sdkmath.NewInt(maxBytes),
	}}
}

func hourlySession(id uint64, status v1base.Status, start, statusAt time.Time, hours, held time.Duration) *nodetypes.Session {
	return &nodetypes.Session{BaseSession: &sessiontypes.BaseSession{
		ID: id, Status: status, StartAt: start, StatusAt: statusAt,
		DownloadBytes: sdkmath.ZeroInt(), UploadBytes: sdkmath.ZeroInt(), MaxBytes: sdkmath.ZeroInt(),
		MaxDuration: hours, Duration: held,
	}}
}

// planSession is a plan session as the session query gives it: its max_bytes
// is the allocation's headroom, G - (U - S), computed by the chain's own code.
func planSession(t *testing.T, id uint64, alloc *v2subscriptiontypes.Allocation, download, upload int64) *subscriptiontypes.Session {
	t.Helper()
	ceiling, err := alloc.SessionCeiling(sdkmath.NewInt(download).Add(sdkmath.NewInt(upload)))
	if err != nil {
		t.Fatal(err)
	}

	return &subscriptiontypes.Session{SubscriptionID: 1, BaseSession: &sessiontypes.BaseSession{
		ID: id, Status: v1base.StatusActive, StartAt: blockTime.Add(-time.Hour),
		DownloadBytes: sdkmath.NewInt(download), UploadBytes: sdkmath.NewInt(upload), MaxBytes: ceiling,
	}}
}

// TestBoundReportMatchesTheChain: for direct byte, direct hourly (active and
// pending) and plan sessions in every relation to their limits, the report
// the node sends passes the chain's own checks, in the block the bound was
// taken at and in a later one: the message's ValidateBasic and counter
// limit, no total below what the chain holds, a direct session's purchase
// and observed time (CheckReport), and a plan session's allocation ceiling
// (SessionCeiling). It claims no more than the peer moved or was served,
// and where it is cut it is cut to the limit exactly.
//
// Rules: [SL-21].
func TestBoundReportMatchesTheChain(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	from := base.NodeAddress(make([]byte, 20)).String()
	n := func(limit int64) int64 { return rng.Int63n(limit) }

	for i := 0; i < 3000; i++ {
		id := uint64(i + 1)
		var (
			session sessiontypes.Session
			item    types.Session
			ceiling sdkmath.Int // the most the chain records for the total, nil for none
			limit   time.Duration
			hourly  *nodetypes.Session
			alloc   *v2subscriptiontypes.Allocation
		)
		switch i % 3 {
		case 0: // a direct byte session
			maxBytes := 1 + n(1<<40)
			heldDown := n(maxBytes + 1)
			heldUp := n(maxBytes - heldDown + 1)
			session = byteSession(id, maxBytes, heldDown, heldUp)
			item = storedRow(id, heldDown+n(maxBytes), heldUp+n(maxBytes), time.Duration(n(int64(time.Hour))))
			ceiling = sdkmath.NewInt(maxBytes)
		case 1: // a direct hourly session, active or pending
			hours := time.Duration(1+n(10)) * time.Hour
			start := blockTime.Add(-time.Duration(n(int64(12 * time.Hour))))
			status, statusAt := v1base.StatusActive, start
			if rng.Intn(2) == 0 {
				status, statusAt = v1base.StatusInactivePending, start.Add(time.Duration(n(int64(blockTime.Sub(start))+1)))
			}
			end := blockTime
			if status.Equal(v1base.StatusInactivePending) {
				end = statusAt
			}
			limit = min(hours, end.Sub(start))
			held := time.Duration(n(int64(limit) + 1))
			hourly = hourlySession(id, status, start, statusAt, hours, held)
			session = hourly
			item = storedRow(id, n(1<<40), n(1<<40), held+time.Duration(n(int64(3*time.Hour))))
		default: // a plan session; every few cases with the allocation used up
			granted := 1 + n(1<<40)
			utilised := n(granted + 1)
			if i%7 == 0 {
				utilised = granted
			}
			heldDown := n(utilised/2 + 1)
			heldUp := n(utilised - heldDown + 1)
			if i%11 == 0 {
				heldDown, heldUp = 0, 0
			}
			alloc = &v2subscriptiontypes.Allocation{ID: 1, Address: "a",
				GrantedBytes: sdkmath.NewInt(granted), UtilisedBytes: sdkmath.NewInt(utilised)}
			session = planSession(t, id, alloc, heldDown, heldUp)
			ceiling = session.GetMaxBytes()
			item = storedRow(id, heldDown+n(granted), heldUp+n(granted), time.Duration(n(int64(time.Hour))))
		}

		r, _, err := boundReport(item, session, at(blockTime))
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		download, upload := sdkmath.NewInt(r.Download), sdkmath.NewInt(r.Upload)
		total := download.Add(upload)

		msg := sessiontypes.NewMsgUpdateSessionRequest(base.NodeAddress(make([]byte, 20)), r.ID, download, upload, r.Duration, nil)
		if msg.From != from {
			t.Fatalf("case %d: from %s", i, msg.From)
		}
		if err := msg.ValidateBasic(); err != nil {
			t.Fatalf("case %d: the chain refuses the message: %v", i, err)
		}
		if download.LT(session.GetDownloadBytes()) || upload.LT(session.GetUploadBytes()) || r.Duration < session.GetDuration() {
			t.Fatalf("case %d: report %+v below what the chain holds", i, r)
		}
		if r.Download > item.Download || r.Upload > item.Upload || r.Duration > item.Duration() {
			t.Fatalf("case %d: report %+v claims more than the row %+v holds", i, r, item)
		}
		if s, ok := session.(*nodetypes.Session); ok {
			for _, landed := range []time.Time{blockTime, blockTime.Add(7 * time.Second)} {
				if err := s.CheckReport(total, r.Duration, landed); err != nil {
					t.Fatalf("case %d: the chain refuses the report: %v", i, err)
				}
			}
		}
		if alloc != nil {
			max, err := alloc.SessionCeiling(session.GetDownloadBytes().Add(session.GetUploadBytes()))
			if err != nil || total.GT(max) {
				t.Fatalf("case %d: total %s past the allocation ceiling %s (%v)", i, total, max, err)
			}
		}

		local := sdkmath.NewInt(item.Download).Add(sdkmath.NewInt(item.Upload))
		if !ceiling.IsNil() && local.GT(ceiling) && !total.Equal(ceiling) {
			t.Fatalf("case %d: a report past the ceiling %s is cut to %s", i, ceiling, total)
		}
		if hourly != nil && item.Duration() > limit && r.Duration != limit {
			t.Fatalf("case %d: duration %s past the limit %s is cut to %s", i, item.Duration(), limit, r.Duration)
		}
		if s, ok := session.(*nodetypes.Session); ok && !ceiling.IsNil() && total.Equal(ceiling) {
			if err := s.CheckReport(total.AddRaw(1), r.Duration, blockTime); err == nil {
				t.Fatalf("case %d: one byte past the cut is accepted, so the cut is not at the limit", i)
			}
		}
	}
}

// TestBoundReportSplitsTheCutByWhatMoved: the headroom above what the chain
// holds goes to download and upload in proportion to what each moved since.
//
// Rules: [SL-21].
func TestBoundReportSplitsTheCutByWhatMoved(t *testing.T) {
	r, send, err := boundReport(storedRow(1, 400, 150, 0), byteSession(1, 250, 100, 50), at(blockTime))
	if err != nil || !send {
		t.Fatalf("send %v, err %v", send, err)
	}
	if r.Download != 175 || r.Upload != 75 {
		t.Fatalf("cut to download %d upload %d, want 175 and 75: 300 and 100 moved, 100 bytes of headroom", r.Download, r.Upload)
	}
}

// TestNothingNewWithinTheLimitsIsNotReported: a session that has reached its
// ceiling, or whose peer idles, gets no report, so the chain's idle timeout
// ends it instead of the node keeping it alive with reports that add
// nothing; an hourly session whose observed time has grown since still
// reports the time it was served.
//
// Rules: [SL-22].
func TestNothingNewWithinTheLimitsIsNotReported(t *testing.T) {
	used := &v2subscriptiontypes.Allocation{ID: 1, Address: "a", GrantedBytes: sdkmath.NewInt(1000), UtilisedBytes: sdkmath.NewInt(1000)}
	start := blockTime.Add(-30 * time.Minute)
	for _, c := range []struct {
		name    string
		item    types.Session
		session sessiontypes.Session
		send    bool
	}{
		{"a plan session whose allocation is used up", storedRow(1, 900, 300, time.Minute),
			planSession(t, 1, used, 0, 0), false},
		{"a plan session that used the allocation itself", storedRow(1, 900, 300, time.Minute),
			planSession(t, 1, used, 700, 300), false},
		{"a byte session at its purchased bytes", storedRow(1, 900, 300, time.Minute),
			byteSession(1, 1000, 700, 300), false},
		{"an hourly session whose peer idles", storedRow(1, 10, 10, 20*time.Minute),
			withBytes(hourlySession(1, v1base.StatusActive, start, start, time.Hour, 20*time.Minute), 10, 10), false},
		{"an hourly session at the time the chain observed", storedRow(1, 10, 10, 40*time.Minute),
			withBytes(hourlySession(1, v1base.StatusActive, start, start, time.Hour, 30*time.Minute), 10, 10), false},
		{"an hourly session the chain has observed longer since", storedRow(1, 10, 10, 40*time.Minute),
			withBytes(hourlySession(1, v1base.StatusActive, start, start, time.Hour, 20*time.Minute), 10, 10), true},
	} {
		_, send, err := boundReport(c.item, c.session, at(blockTime))
		if err != nil || send != c.send {
			t.Errorf("%s: send %v, err %v; want send %v", c.name, send, err, c.send)
		}
	}
}

func withBytes(s *nodetypes.Session, download, upload int64) *nodetypes.Session {
	s.DownloadBytes, s.UploadBytes = sdkmath.NewInt(download), sdkmath.NewInt(upload)
	return s
}

// boundChain is fakeChain holding concrete sessions, with a plan session's
// subscription active.
type boundChain struct {
	*fakeChain
	held     map[uint64]sessiontypes.Session
	timeErr  error
	refuseID uint64 // a report naming it is refused, as over its quota
}

func (c *boundChain) QuerySession(id uint64) (sessiontypes.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.held[id]
	if !ok {
		return nil, nil
	}

	return s, nil
}

func (c *boundChain) QuerySubscription(id uint64) (*subscriptiontypes.Subscription, error) {
	return &subscriptiontypes.Subscription{ID: id, Status: v1base.StatusActive}, nil
}

func (c *boundChain) LatestBlockTime() (time.Time, error) {
	if c.timeErr != nil {
		return time.Time{}, c.timeErr
	}

	return c.fakeChain.LatestBlockTime()
}

func (c *boundChain) UpdateSessions(items ...types.Report) error {
	for _, item := range items {
		if item.ID == c.refuseID {
			c.mu.Lock()
			c.sent = append(c.sent, []uint64{item.ID})
			c.mu.Unlock()
			return errors.New("insufficient bytes")
		}
	}

	return c.fakeChain.UpdateSessions(items...)
}

func boundRig(t *testing.T, rows []types.Session, held map[uint64]sessiontypes.Session) (*Node, *boundChain, *peerService) {
	t.Helper()
	db := testDB(t)
	service := &peerService{peers: map[string]types.Peer{}}
	statuses := map[uint64]v1base.Status{}
	for _, r := range rows {
		db.Create(&r)
		service.peers[r.Key] = types.Peer{Key: r.Key}
	}
	for id, s := range held {
		statuses[id] = s.GetStatus()
	}
	chain := &boundChain{fakeChain: &fakeChain{sessions: statuses, now: blockTime}, held: held}
	n := NewNode(context.NewContext().WithDatabase(db).WithLogger(cmtlog.NewNopLogger()).WithService(service))
	n.chain = chain

	return n, chain, service
}

// TestAPlanSessionWithoutHeadroomLosesItsPeer: the session query gives a
// plan session's headroom as its max_bytes, and zero means none is left.
// Read as no cap, it let a peer go on being served on an allocation used up
// elsewhere, with nothing the chain would record for it.
//
// Rules: [SL-9].
func TestAPlanSessionWithoutHeadroomLosesItsPeer(t *testing.T) {
	used := &v2subscriptiontypes.Allocation{ID: 1, Address: "a", GrantedBytes: sdkmath.NewInt(1000), UtilisedBytes: sdkmath.NewInt(1000)}
	n, chain, service := boundRig(t,
		[]types.Session{storedRow(1, 10, 10, time.Minute)},
		map[uint64]sessiontypes.Session{1: planSession(t, 1, used, 0, 0)})

	if err := n.updateSessions(); err != nil {
		t.Fatal(err)
	}
	if !service.wasRemoved(key(1)) {
		t.Error("the peer of a plan session with no headroom left was left connected")
	}
	if len(chain.sent) != 0 {
		t.Errorf("sent %v: a report the allocation cannot record", chain.sent)
	}
}

// TestAPendingHourlySessionIsBilledToItsCancellation: once a session is
// pending, the chain bills an hourly one to the time it was cancelled, and
// the final-report grace is not billable time. The final report is still
// sent, cut to that time, without asking the chain for the time.
//
// Rules: [SL-21], [SL-22].
func TestAPendingHourlySessionIsBilledToItsCancellation(t *testing.T) {
	start := time.Now().Add(-2 * time.Hour)
	n, chain, _ := boundRig(t,
		[]types.Session{storedRow(1, 10, 10, 90*time.Minute)},
		map[uint64]sessiontypes.Session{1: hourlySession(1, v1base.StatusInactivePending, start, start.Add(time.Hour), 3*time.Hour, 30*time.Minute)})
	chain.timeErr = errors.New("no remote answers")

	if err := n.updateSessions(); err != nil {
		t.Fatal(err)
	}
	if len(chain.reports) != 1 || chain.reports[0].Duration != time.Hour {
		t.Fatalf("reports %+v, want one, for the hour up to the cancellation", chain.reports)
	}
}

// TestAnHourlyReportWaitsForTheChainTime: an active hourly session's bound
// needs the latest block time. When no remote gives it, that report waits
// for the next pass; the others go.
//
// Rules: [SL-21].
func TestAnHourlyReportWaitsForTheChainTime(t *testing.T) {
	start := blockTime.Add(-time.Hour)
	n, chain, _ := boundRig(t,
		[]types.Session{storedRow(1, 10, 10, 30*time.Minute), storedRow(2, 10, 10, 0)},
		map[uint64]sessiontypes.Session{
			1: hourlySession(1, v1base.StatusActive, start, start, 3*time.Hour, 0),
			2: byteSession(2, 1000, 0, 0),
		})
	chain.timeErr = errors.New("no remote answers")

	if err := n.updateSessions(); err == nil {
		t.Fatal("a report that could not be bounded was not reported as an error")
	}
	if len(chain.accepted) != 1 || chain.accepted[0] != 2 {
		t.Fatalf("reported %v, want only the byte session", chain.accepted)
	}
}

// TestARefusedReportIsTriedOnlyWhileTheChainKeepsTheSession: a report the
// chain refuses, as over its quota when another session used the allocation
// between the query and the send, is tried again on the next passes, each
// with a fresh bound, and not once the chain has ended the session: its row
// goes then.
//
// Rules: [SL-22].
func TestARefusedReportIsTriedOnlyWhileTheChainKeepsTheSession(t *testing.T) {
	n, chain, _ := boundRig(t,
		[]types.Session{storedRow(1, 10, 10, 0)},
		map[uint64]sessiontypes.Session{1: byteSession(1, 1000, 0, 0)})
	chain.refuseID = 1

	for pass := 0; pass < 2; pass++ {
		_ = n.updateSessions()
	}
	if len(chain.sent) < 2 {
		t.Fatalf("sent %v: the refused report was not tried on the next pass", chain.sent)
	}

	ended := byteSession(1, 1000, 0, 0)
	ended.Status = v1base.StatusInactive
	chain.mu.Lock()
	chain.held[1] = ended
	chain.mu.Unlock()
	tried := len(chain.sent)
	for pass := 0; pass < 2; pass++ {
		if err := n.updateSessions(); err != nil {
			t.Fatal(err)
		}
	}
	if len(chain.sent) != tried {
		t.Fatalf("sent %v: a report for a session the chain has ended", chain.sent[tried:])
	}
	var count int64
	n.Database().Model(&types.Session{}).Count(&count)
	if count != 0 {
		t.Fatalf("%d rows left: the ended session's row stays", count)
	}
}

// TestARestartReportsAPendingSession: the chain takes a pending session's
// final report until its grace ends, so the start after a restart reports
// what such a session moved too, cut like every report, and still not an
// ended one's.
//
// Rules: [SL-14], [SL-21].
func TestARestartReportsAPendingSession(t *testing.T) {
	pending := byteSession(2, 1000, 0, 0)
	pending.Status = v1base.StatusInactivePending
	ended := byteSession(3, 1000, 0, 0)
	ended.Status = v1base.StatusInactive
	n, chain, _ := boundRig(t,
		[]types.Session{storedRow(1, 10, 10, 0), storedRow(2, 900, 300, 0), storedRow(3, 10, 10, 0)},
		map[uint64]sessiontypes.Session{1: byteSession(1, 1000, 0, 0), 2: pending, 3: ended})

	if err := n.ReconcileSessions(); err != nil {
		t.Fatal(err)
	}
	got := map[uint64]types.Report{}
	for _, r := range chain.reports {
		got[r.ID] = r
	}
	if len(got) != 2 || got[1].Download != 10 || got[2].Download+got[2].Upload != 1000 {
		t.Fatalf("reported %+v, want the active session and the pending one, cut to its 1000 bytes", chain.reports)
	}
}

// TestARestartReportsTheOthersWhenTheChainRefusesOne: the start sends its
// final reports the way the usage job does, so a pending session whose grace
// ran out between the query and the send does not take the active sessions'
// final reports down with it.
//
// Rules: [SL-11], [SL-14].
func TestARestartReportsTheOthersWhenTheChainRefusesOne(t *testing.T) {
	pending := byteSession(2, 1000, 0, 0)
	pending.Status = v1base.StatusInactivePending
	n, chain, _ := boundRig(t,
		[]types.Session{storedRow(1, 10, 10, 0), storedRow(2, 10, 10, 0)},
		map[uint64]sessiontypes.Session{1: byteSession(1, 1000, 0, 0), 2: pending})
	chain.refuseID = 2

	if err := n.ReconcileSessions(); err != nil {
		t.Fatal(err)
	}
	if len(chain.accepted) != 1 || chain.accepted[0] != 1 {
		t.Fatalf("accepted %v, want the active session's final report", chain.accepted)
	}
}

// TestBoundReportNeedsNoTimeForOtherSessions: only an active hourly session
// asks for the chain time.
func TestBoundReportNeedsNoTimeForOtherSessions(t *testing.T) {
	fail := func() (time.Time, error) { return time.Time{}, errors.New("asked") }
	if _, _, err := boundReport(storedRow(1, 10, 10, 0), byteSession(1, 1000, 0, 0), fail); err != nil {
		t.Fatalf("a byte session asked for the time: %v", err)
	}
	if _, _, err := boundReport(storedRow(1, math.MaxInt64, 10, 0), byteSession(1, 1000, 0, 0), fail); err != nil {
		t.Fatalf("a byte session asked for the time: %v", err)
	}
}
