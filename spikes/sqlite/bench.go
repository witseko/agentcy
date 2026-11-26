package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

const readSQL = `SELECT message.id, participant.label, message.body, message.kind, recipient.label AS "to", message.reply_to, message.due_minutes, participant.role AS author_role, message.recipient = ? OR message.recipient IS NULL AND message.kind IS NOT ? AND message.kind IS NOT ? AS wake, blocker_task.id IS NOT NULL AS task_blocker
FROM message JOIN participant ON message.author = participant.id LEFT OUTER JOIN participant AS recipient ON message.recipient = recipient.id LEFT OUTER JOIN message AS blocker_task ON message.kind = ? AND blocker_task.id = message.reply_to AND blocker_task.room = message.room AND blocker_task.kind = ?
WHERE message.room = ? AND message.id > ? AND (message.author != ? OR message.kind IN (?, ?)) ORDER BY message.id
 LIMIT ? OFFSET ?`

var benchQueries = []string{
	"retry", "retry behavior", "naïve café", "Zürich Müller", "日本語 テスト", "привет мир",
	"don't stop", "state-of-the-art", "C++ templates", "foo.bar baz_qux", "the quick brown fox",
	"review plan", "x", "migration schema version", "alpha beta", "cursor page",
}

func summarize(name string, samples []time.Duration) {
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	median := float64(samples[len(samples)/2].Nanoseconds()) / 1000
	p95 := float64(samples[len(samples)*95/100].Nanoseconds()) / 1000
	fmt.Printf("%s: median=%.1fus p95=%.1fus n=%d\n", name, median, p95, len(samples))
}

// bench times the read, insert and search queries against a store.
func bench(driver, store string, n int) error {
	ctx := context.Background()
	reader, err := open(driver, store, "")
	if err != nil {
		return err
	}
	defer reader.Close()
	writer, err := open(driver, store, "immediate")
	if err != nil {
		return err
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	var room string
	var author, top int64
	if err := reader.QueryRowContext(ctx, "SELECT id FROM conversation LIMIT 1").Scan(&room); err != nil {
		return err
	}
	if err := reader.QueryRowContext(ctx, "SELECT id FROM participant WHERE label='Reviewer'").Scan(&author); err != nil {
		return err
	}
	if err := reader.QueryRowContext(ctx, "SELECT max(id) FROM message").Scan(&top); err != nil {
		return err
	}
	var version string
	_ = reader.QueryRowContext(ctx, "select sqlite_version()").Scan(&version)
	fmt.Println("go driver", driver, "sqlite", version, "messages", top)

	samples := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		after := (int64(i) * 7919) % (top - 20)
		start := time.Now()
		rows, err := reader.QueryContext(ctx, readSQL, author, "blocker", "event", "blocker", "task", room, after, author, "event", "proposal", 17, 0)
		if err != nil {
			return err
		}
		if err := drain(rows); err != nil {
			return err
		}
		samples = append(samples, time.Since(start))
	}
	summarize("read", samples)

	samples = samples[:0]
	for i := 0; i < n; i++ {
		start := time.Now()
		tx, err := writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO message (room, author, body, created) VALUES (?, ?, ?, ?)", room, author, fmt.Sprintf("go bench insert %d retry alpha", i), 1); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		samples = append(samples, time.Since(start))
	}
	summarize("insert", samples)

	samples = samples[:0]
	for i := 0; i < n; i++ {
		expr := matchExpression(benchQueries[i%len(benchQueries)])
		start := time.Now()
		rows, err := reader.QueryContext(ctx, searchSQL, 1, 81, 0, "", "", "…", 12, expr, 50, 0)
		if err != nil {
			return err
		}
		if err := drain(rows); err != nil {
			return err
		}
		samples = append(samples, time.Since(start))
	}
	summarize("search", samples)
	return nil
}

// drain reads every column of every row, as a real caller would.
func drain(rows *sql.Rows) error {
	defer rows.Close()
	cols, _ := rows.Columns()
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
	}
	return rows.Err()
}
