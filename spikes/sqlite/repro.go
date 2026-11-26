package main

import (
	"context"
	"database/sql"
	"fmt"
)

func integ(ctx context.Context, label string, db *sql.DB) {
	var s string
	err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&s)
	fmt.Printf("%-50s integrity=%q err=%v\n", label, s, err)
}

// repro bisects which step makes integrity_check report an FTS5 checksum mismatch.
func repro(driver, store string) error {
	ctx := context.Background()
	a, _ := open(driver, store, "")
	b, _ := open(driver, store, "immediate")
	defer a.Close()
	defer b.Close()
	a.SetMaxOpenConns(1)
	integ(ctx, "A fresh", a)
	ins := func(db *sql.DB, body string) {
		_, err := db.ExecContext(ctx, "INSERT INTO message (room, author, body, created) VALUES ((SELECT id FROM conversation LIMIT 1), 1, ?, 1)", body)
		if err != nil {
			fmt.Println("insert error", err)
		}
	}
	ins(a, "plain ascii words only")
	integ(ctx, "A after A inserts ascii", a)
	ins(a, "ünïcode wörds")
	integ(ctx, "A after A inserts non-ascii", a)
	ins(b, "another ascii from b")
	integ(ctx, "A after B inserts ascii", a)
	integ(ctx, "B", b)
	return nil
}
