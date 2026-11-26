// Package tokens tracks token spend: the bridge's own estimates, exact usage reported by
// members and plugins, per-model budgets from tokens.json, the user-level ledger of
// spend across conversations, and the balance guidance for leads. It is a port of
// the former Python core's tokens.
//
// Functions that touch a workspace store take the *sql.Tx of a store.Write or store.Read
// call; ledger functions take the ledger *store.Store from Registry.OpenLedger in package
// store and run their own transaction. Time is always passed in as Unix seconds. Results that reach JSON
// have a Value method that keeps Python's field order and spells floats like Python
// (FloatValue), so 75.0 stays 75.0.
//
// Example, recording a read and building a member's status usage:
//
//	err := workspace.Write(ctx, func(tx *sql.Tx) error {
//		snapshot, err := tokens.AddEstimated(ctx, tx, room, slot, tokens.EstimateRead(bodies, brief), 0, now)
//		if err != nil {
//			return err
//		}
//		return tokens.SyncLedger(ctx, ledger, workspaceKey, snapshot)
//	})
//
//	settings, err := settingsFile.Load() // on error Python logs a warning and uses nil
//	slots, err := tokens.RoomUsage(ctx, tx, room)
//	spend, err := tokens.ModelSpend(ctx, ledger, models, sinceByModel)
//	usage := tokens.BuildMemberUsage(slots[0], settings, spend) // nil: no "usage" entry
//	entry.Set("usage", usage.Value())
//
// The ledger write in SyncLedger is synchronous for now; port plan fix L6 moves it off
// the request path later.
package tokens
