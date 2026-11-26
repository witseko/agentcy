// Command sqlitespike checks whether a CGO-free SQLite driver can serve the agentcy store.
//
// Usage:
//
//	sqlitespike -driver modernc verify -store PATH -expected expected.json -ddl ddl.json -work DIR
//	sqlitespike -driver modernc bench  -store PATH -n 1000
//	sqlitespike hold -store PATH -seconds 2     (second writer process, used by check e)
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"net/url"
	"os"
)

// dsn builds a data source name for the chosen driver.
// txlock is "" (deferred) or "immediate". The path goes through net/url so that a store
// directory containing '#', '?' or '%' is escaped and still opens the right file.
func dsn(driver, path, txlock string) string {
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "journal_mode(WAL)")
	if txlock != "" {
		query.Set("_txlock", txlock)
	}
	if driver != "modernc" && driver != "ncruces" {
		panic("unknown driver " + driver)
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}
	return u.String()
}

func driverName(driver string) string {
	if driver == "ncruces" {
		return "sqlite3"
	}
	return "sqlite"
}

func open(driver, path, txlock string) (*sql.DB, error) {
	return sql.Open(driverName(driver), dsn(driver, path, txlock))
}

func main() {
	driver := flag.String("driver", "modernc", "modernc or ncruces")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "need a subcommand: verify, bench, hold")
		os.Exit(2)
	}
	sub := flag.NewFlagSet(args[0], flag.ExitOnError)
	store := sub.String("store", "", "store path")
	expected := sub.String("expected", "", "expected.json from make_store.py")
	ddl := sub.String("ddl", "", "ddl.json from make_store.py")
	work := sub.String("work", "", "scratch directory")
	n := sub.Int("n", 1000, "runs per benchmark")
	seconds := sub.Float64("seconds", 2, "hold time")
	sub.Parse(args[1:])
	var err error
	switch args[0] {
	case "verify":
		err = verify(*driver, *store, *expected, *ddl, *work)
	case "bench":
		err = bench(*driver, *store, *n)
	case "fts":
		err = ftsCheck(*driver, *store)
	case "repro":
		err = repro(*driver, *store)
	case "hold":
		err = hold(*driver, *store, *seconds)
	default:
		err = fmt.Errorf("unknown subcommand %q", args[0])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
