package conversation

import (
	"context"
	"database/sql"

	"github.com/witseko/agentcy/internal/tokens"
	"github.com/witseko/agentcy/internal/wire"
)

// recordReportedUsage validates the member and records its exact cumulative token usage
// inside a write transaction (buffering the ledger sync for post-commit).
func recordReportedUsage(ctx context.Context, tx *sql.Tx, book *usageBook, member *participant, request *wire.ReportUsage, now int64) error {
	if member.Retired {
		return reject(retiredError)
	}
	if !member.Open {
		return reject(conversationClosed)
	}
	snapshot, err := tokens.RecordExact(ctx, tx, member.Room, member.ID, tokens.ExactUsage{
		InputTokens: request.InputTokens, OutputTokens: request.OutputTokens,
		Source: tokens.SourceReported, Model: request.Model,
	}, now)
	if err != nil {
		return err
	}
	return book.sync(ctx, request.Workspace, snapshot)
}

// reportUsageSummary builds the report_usage response from a committed status snapshot
// (so the user-level token ledger already reflects the newly synced report).
func reportUsageSummary(ctx context.Context, tx *sql.Tx, book *usageBook, member *participant, now int64) (wire.Value, error) {
	current, err := status(ctx, tx, book, member, now)
	if err != nil {
		return wire.Value{}, err
	}
	result := singleField("participant", wire.TextValue(member.Label))
	if current.own != nil && current.own.usage != nil {
		result.Set("usage", current.own.usage.Value())
	}
	if current.balance != nil {
		result.Set("token_balance", current.balance.Value())
	}
	return result, nil
}
