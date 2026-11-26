package conversation

import (
	"context"
	"database/sql"

	"github.com/witseko/agentcy/internal/tokens"
	"github.com/witseko/agentcy/internal/wire"
)

// reportUsage records the exact cumulative token usage that a member reports, then
// answers with its usage summary and the team balance (report_usage in repository.py).
func reportUsage(ctx context.Context, tx *sql.Tx, book *usageBook, member *participant, request *wire.ReportUsage, now int64) (wire.Value, error) {
	if member.Retired {
		return wire.Value{}, reject(retiredError)
	}
	if !member.Open {
		return wire.Value{}, reject(conversationClosed)
	}
	snapshot, err := tokens.RecordExact(ctx, tx, member.Room, member.ID, tokens.ExactUsage{
		InputTokens: request.InputTokens, OutputTokens: request.OutputTokens,
		Source: tokens.SourceReported, Model: request.Model,
	}, now)
	if err != nil {
		return wire.Value{}, err
	}
	if err := book.sync(ctx, request.Workspace, snapshot); err != nil {
		return wire.Value{}, err
	}
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
