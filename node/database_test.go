// SPDX-License-Identifier: Apache-2.0

package node

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"github.com/Feryx-Official/dvpnd/v9/types"
)

func closeDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestDatabaseFileIsPrivate: the session database holds wallet addresses
// and proxy UUIDs, so its file is readable by its owner only, a file an
// earlier release left readable by others included. Only the 0700 home
// directory protected it before.
//
// Rules: [PV-6].
func TestDatabaseFileIsPrivate(t *testing.T) {
	for name, existing := range map[string]bool{"new": false, "left readable by others": true} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "data.db")
			if existing {
				if err := os.WriteFile(path, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			db, err := OpenDatabase(path)
			if err != nil {
				t.Fatal(err)
			}
			db.Create(&types.Session{ID: 1, Key: "k", Address: "a"})
			closeDB(t, db)

			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("mode %o, want 600", info.Mode().Perm())
			}
		})
	}
}

// TestDatabaseSettings: the settings the node depends on are set where it
// opens the file, not left to the driver's defaults: secure_delete (PV-4),
// a rollback journal deleted after each write (a write-ahead log would keep
// a deleted row's content until a checkpoint), and five seconds' wait for a
// lock held by another of the pool's connections, rather than failing at
// once.
//
// Rules: [PV-4].
func TestDatabaseSettings(t *testing.T) {
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeDB(t, db) })

	for pragma, want := range map[string]string{"secure_delete": "1", "journal_mode": "delete", "busy_timeout": "5000"} {
		var got string
		if err := db.Raw("PRAGMA " + pragma).Scan(&got).Error; err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %s, want %s", pragma, got, want)
		}
	}
}

// TestADeletedSessionIsGone: a session row deleted the plain way is gone
// from the file, its wallet address and peer key with it, and its id can
// be used again. With gorm's soft delete a plain delete only stamped
// deleted_at, and every delete had to remember Unscoped.
//
// Rules: [PV-4].
func TestADeletedSessionIsGone(t *testing.T) {
	const address, key = "sent1softdeletemarker", "softdeletepeerkey"

	path := filepath.Join(t.TempDir(), "data.db")
	db, err := OpenDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&types.Session{ID: 1, Key: key, Address: address}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&types.Session{}, 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&types.Session{ID: 1, Key: "other", Address: "other"}).Error; err != nil {
		t.Errorf("the id of a deleted session cannot be used again: %v", err)
	}
	closeDB(t, db)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(address)) || bytes.Contains(raw, []byte(key)) {
		t.Errorf("the deleted session is still in the file")
	}
}
