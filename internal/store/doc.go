// Package store opens the SQLite databases of agentcy: one store per workspace and the
// user-level token ledger. It is a port of the former Python core's storage/database.py and the ledger
// setup in its tokens/ledger.py, and holds no conversation logic.
//
// A Registry opens the stores of one data directory: Registry.Open returns the store of
// a workspace key and creates it at schema version 15 when the file is missing, and
// Registry.OpenLedger returns the token ledger. The daemon creates one Registry and
// passes it to every part that opens stores. Each store has two connection pools: Write runs a function in a
// BEGIN IMMEDIATE transaction under the store's one write mutex, and Read runs a function
// in a plain transaction that never blocks writers. Both commit or roll back for the
// caller, so a function only runs its statements and returns an error to abort.
//
// Example:
//
//	dataDir, _ := paths.DataDir(os.LookupEnv(paths.HomeVariable))
//	stores := store.NewRegistry(dataDir)
//	defer stores.Close()
//	workspace, err := stores.Open(ctx, key)
//	if err != nil {
//		return err
//	}
//	err = workspace.Write(ctx, func(tx *sql.Tx) error {
//		_, err := tx.ExecContext(ctx, "UPDATE participant SET seen = ? WHERE id = ?", now, slot)
//		return err
//	})
//
// Stores stay open for the life of the Registry (Python caches the engine per workspace
// the same way); Registry.Close closes them at shutdown.
package store
