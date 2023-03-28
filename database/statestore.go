package database

import (
	"embed"

	"maunium.net/go/mautrix/util/dbutil"
)

//go:embed *.sql
var rawUpgrades embed.FS

var UpgradeTable dbutil.UpgradeTable

func init() {
	UpgradeTable.RegisterFS(rawUpgrades)
}

type Database struct {
	DB *dbutil.Database
}

func NewDatabase(db *dbutil.Database) *Database {
	db.UpgradeTable = UpgradeTable
	return &Database{DB: db}
}
