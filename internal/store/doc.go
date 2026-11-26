// Package store opens the SQLite databases of agentcy: one store per workspace and the
// user-level token ledger. It is a port of src/agentcy/storage/database.py and the ledger
// setup in src/agentcy/tokens/ledger.py, and holds no conversation logic.
//
// Open returns the store of a workspace key and creates it at schema version 15 when the
// file is missing. Each store has two connection pools: Write runs a function in a
// BEGIN IMMEDIATE transaction under the store's one write mutex, and Read runs a function
// in a plain transaction that never blocks writers. Both commit or roll back for the
// caller, so a function only runs its statements and returns an error to abort.
//
// Example:
//
//	dataDir, _ := paths.DataDir(os.LookupEnv(paths.HomeVariable))
//	workspace, err := store.Open(ctx, dataDir, key)
//	if err != nil {
//		return err
//	}
//	err = workspace.Write(ctx, func(tx *sql.Tx) error {
//		_, err := tx.ExecContext(ctx, "UPDATE participant SET seen = ? WHERE id = ?", now, slot)
//		return err
//	})
//
// Stores stay open for the life of the process (Python caches the engine per workspace
// the same way); CloseAll closes them at shutdown.
package store
