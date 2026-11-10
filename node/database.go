// SPDX-License-Identifier: Apache-2.0

package node

import (
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/Feryx-Official/dvpnd/v9/types"
)

// OpenDatabase opens the session database at path and migrates its table. It
// is the only place the database is opened, so what it is opened with holds
// for the node and for the tests alike: secure_delete zeroes deleted rows, so
// a session's wallet address and peer key do not linger in free pages of the
// file.
func OpenDatabase(path string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(path+"?_secure_delete=on"), &gorm.Config{
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
