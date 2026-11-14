// SPDX-License-Identifier: Apache-2.0

package node

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"
	v1base "github.com/Feryx-Official/feryxhub/v12/types/v1"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/Feryx-Official/dvpnd/v9/context"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

type fakeSession struct {
	status         v1base.Status
	upload, downld int64
}

func (f fakeSession) GetStatus() v1base.Status      { return f.status }
func (f fakeSession) GetUploadBytes() sdkmath.Int   { return sdkmath.NewInt(f.upload) }
func (f fakeSession) GetDownloadBytes() sdkmath.Int { return sdkmath.NewInt(f.downld) }

// Rules: [SL-14].
func TestSessionNeedsReport(t *testing.T) {
	cases := []struct {
		name  string
		local types.Session
		chain fakeSession
		want  bool
	}{
		{"active and moved data", types.Session{Upload: 10, Download: 20},
			fakeSession{v1base.StatusActive, 0, 0}, true},
		{"active but already reported", types.Session{Upload: 10, Download: 20},
			fakeSession{v1base.StatusActive, 10, 20}, false},
		{"active, only download differs", types.Session{Upload: 10, Download: 25},
			fakeSession{v1base.StatusActive, 10, 20}, true},
		{"inactive is never reported", types.Session{Upload: 10, Download: 20},
			fakeSession{v1base.StatusInactive, 0, 0}, false},
	}
	for _, c := range cases {
		if got := sessionNeedsReport(c.local, c.chain); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// Rules: [SL-14].
func TestClearSessions(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&types.Session{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&types.Session{ID: 1, Key: "a", Address: "addr1"})
	db.Create(&types.Session{ID: 2, Key: "b", Address: "addr2", Upload: 5, Download: 7})

	n := NewNode(context.NewContext().WithDatabase(db).WithLogger(cmtlog.NewNopLogger()))

	if err := n.clearSessions(); err != nil {
		t.Fatal(err)
	}

	var count int64
	n.Database().Model(&types.Session{}).Count(&count)
	if count != 0 {
		t.Fatalf("sessions remain after clear: %d", count)
	}
	// A hard (unscoped) delete must leave nothing even for soft-delete queries.
	n.Database().Unscoped().Model(&types.Session{}).Count(&count)
	if count != 0 {
		t.Fatalf("soft-deleted rows remain: %d", count)
	}
}

// TestDeletedSessionsLeaveNoTrace checks the database file itself, not only
// the table: a cleared session's wallet address and peer key must be gone
// from the bytes on disk. A file opened with secure_delete is clean at once;
// one written without it (by an earlier release) still holds them until
// compactDatabase runs. The node's file is opened with OpenDatabase, the one
// door start uses too.
//
// Rules: [PV-4].
func TestDeletedSessionsLeaveNoTrace(t *testing.T) {
	const address, key = "sent1tracemarkeraddress", "tracemarkerpeerkey"

	// earlierRelease opens the file as releases before secure_delete did.
	earlierRelease := func(path string) (*gorm.DB, error) {
		db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: gormlogger.Discard})
		if err != nil {
			return nil, err
		}
		return db, db.AutoMigrate(&types.Session{})
	}
	for _, tc := range []struct {
		name      string
		open      func(path string) (*gorm.DB, error)
		compact   bool
		wantTrace bool
	}{
		{name: "opened as the node opens it", open: OpenDatabase},
		{name: "earlier release, compacted", open: earlierRelease, compact: true},
		{name: "earlier release, not compacted", open: earlierRelease, wantTrace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "data.db")
			db, err := tc.open(path)
			if err != nil {
				t.Fatal(err)
			}
			db.Create(&types.Session{ID: 1, Key: key, Address: address})

			n := NewNode(context.NewContext().WithDatabase(db).WithLogger(cmtlog.NewNopLogger()))
			if err := n.clearSessions(); err != nil {
				t.Fatal(err)
			}
			if tc.compact {
				n.compactDatabase()
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			if err := sqlDB.Close(); err != nil {
				t.Fatal(err)
			}

			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			trace := bytes.Contains(raw, []byte(address)) || bytes.Contains(raw, []byte(key))
			if trace != tc.wantTrace {
				t.Fatalf("deleted session left in the file: %v, want %v", trace, tc.wantTrace)
			}
		})
	}
}

// TestRowsOfThePreviousReleaseLoad: the session table as 9.4.2 wrote it, with
// a row in it, opens the way the node opens it and the row reads back, so the
// start after an upgrade can still report what the previous run moved. The
// columns added since (max_duration) are empty in such a row and read as
// zero: a session without recorded hours.
//
// Rules: [SL-14].
func TestRowsOfThePreviousReleaseLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	old, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"CREATE TABLE `sessions` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime," +
			"`deleted_at` datetime,`subscription` integer,`key` text,`address` text,`available` integer," +
			"`download` integer,`upload` integer,`base_download` integer,`base_upload` integer,`base_duration` integer)",
		"INSERT INTO `sessions` (`id`,`created_at`,`updated_at`,`key`,`address`,`download`,`upload`,`base_duration`) " +
			"VALUES (7, '2026-01-02 03:04:05', '2026-01-02 04:04:05', 'YQ==', 'a', 300, 200, 60000000000)",
	} {
		if err := old.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}
	if sqlDB, err := old.DB(); err == nil {
		_ = sqlDB.Close()
	}

	db, err := OpenDatabase(path)
	if err != nil {
		t.Fatalf("opening the previous release's table: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	var items []types.Session
	if err := db.Model(&types.Session{}).Find(&items).Error; err != nil {
		t.Fatalf("reading the previous release's row: %v", err)
	}
	if len(items) != 1 || items[0].ID != 7 || items[0].Download != 300 || items[0].Upload != 200 ||
		items[0].Duration() != time.Hour+time.Minute {
		t.Fatalf("read back %+v", items)
	}
	if items[0].MaxDuration != 0 || items[0].PaidTimeUsed(time.Now()) {
		t.Fatalf("a row without recorded hours reads as hours %d", items[0].MaxDuration)
	}
}

// TestReconcileFailsOnAnUnreadableTable: a table the node cannot read is not
// taken for an empty one. Its rows would stay and refuse every reconnect
// with 409 until the chain ended the sessions.
//
// Rules: [SL-14].
func TestReconcileFailsOnAnUnreadableTable(t *testing.T) {
	n, _, db := usageRig(t, nil)
	if err := db.Migrator().DropTable(&types.Session{}); err != nil {
		t.Fatal(err)
	}

	if err := n.ReconcileSessions(); err == nil {
		t.Fatal("a session table that cannot be read was reconciled as empty")
	}
}
