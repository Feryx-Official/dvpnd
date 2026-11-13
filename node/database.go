// SPDX-License-Identifier: Apache-2.0

package node

import (
	"os"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/Feryx-Official/dvpnd/v9/types"
)

// databaseOptions are the settings the node depends on, set here rather than
// left to the driver's defaults. secure_delete zeroes deleted rows, so a
// session's wallet address and peer key do not linger in free pages of the
// file. The rollback journal is deleted after each write; a write-ahead log
// would keep a deleted row's content until a checkpoint. A connection waits
// up to five seconds for a lock another of the pool's connections holds.
const databaseOptions = "?_secure_delete=on&_journal_mode=DELETE&_busy_timeout=5000"

// OpenDatabase opens the session database at path and migrates its table. It
// is the only place the database is opened, so what it is opened with holds
// for the node and for the tests alike. The file holds wallet addresses and
// proxy UUIDs, so it is readable by its owner only, and narrowed when an
// earlier release left it wider; SQLite gives its journal the same mode.
func OpenDatabase(path string) (*gorm.DB, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // G304: the session database in the node's home
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}

	db, err := gorm.Open(sqlite.Open(path+databaseOptions), &gorm.Config{
		Logger:      logger.Discard,
		PrepareStmt: false,
	})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&types.Session{}); err != nil {
		return nil, err
	}

	return db, nil
}
