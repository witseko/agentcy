package store

import (
	_ "embed"
	"errors"
)

// SchemaVersion is the only workspace store version this package opens. Python migrated
// versions 1 to 14 forward; the Go port refuses them (see the port plan, assumption 1).
const SchemaVersion = 15

// LedgerSchemaVersion is the only token ledger version this package opens.
const LedgerSchemaVersion = 1

// LedgerFilename is the name of the user-level token ledger inside the data directory.
const LedgerFilename = "token_ledger.sqlite3"

// storeSuffix ends the file name of every workspace store.
const storeSuffix = ".sqlite3"

// workspaceSchema is the DDL of a store that the Python core created at version 15,
// dumped at commit 17eb9ce, the last commit with the Python core. The full-text index
// makes its own shadow tables, and SQLite makes the indexes behind UNIQUE and PRIMARY
// KEY constraints.
//
//go:embed schema.sql
var workspaceSchema string

// ledgerSchema is the DDL of the token ledger, dumped the same way.
//
//go:embed ledger.sql
var ledgerSchema string

// The errors for a database written by a schema this package does not know. The texts
// are the ones Python raises.
var (
	ErrUnsupportedVersion       = errors.New("Unsupported bridge database version")
	ErrUnsupportedLedgerVersion = errors.New("Unsupported token ledger database version")
)

// ErrInvalidWorkspaceKey is returned for a key that is not 64 lowercase hex characters,
// so that a key can never name a file outside the data directory.
var ErrInvalidWorkspaceKey = errors.New("invalid workspace key")
