package store

import "net/url"

// connectionString returns the modernc.org/sqlite DSN for a database file. Every new
// connection gets the PRAGMAs that Python sets: a 5 second busy timeout, foreign keys and
// WAL. immediate makes every transaction begin with BEGIN IMMEDIATE, for the write pool.
//
// The DSN is built with net/url because SQLite reads "#" and "?" in a plain "file:" path
// as the end of the file name and silently opens another file.
func connectionString(path string, immediate bool) string {
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	if immediate {
		query.Add("_pragma", "journal_mode(WAL)")
		query.Set("_txlock", "immediate")
	}
	location := url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}
	return location.String()
}
