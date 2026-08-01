// zzqaseed inspects and seeds the hub mirror for QA of the PREFIX-n retirement sweep.
package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	db, err := sql.Open("sqlite", os.Args[1]+"?_pragma=busy_timeout(10000)&_txlock=immediate")
	if err != nil {
		panic(err)
	}
	defer func() { _ = db.Close() }()
	switch os.Args[2] {
	case "roots":
		list(db, `SELECT DISTINCT repo, '' FROM issues`)
	case "show":
		list(db, `SELECT identifier, deleted_at FROM issues WHERE repo = '`+os.Args[3]+
			`' AND source = 'azure' ORDER BY identifier`)
	case "seed":
		exec(db, `INSERT INTO issues (repo, source, identifier, title, status, status_group,
			external_id, created_at, updated_at, deleted_at) VALUES ('`+os.Args[3]+
			`', 'azure', 'CON-6694', 'Widen the id contract to bare numbers', 'New', 'unstarted',
			'6694', '2026-07-01T09:00:00Z', '2026-07-30T11:22:33Z', '')`)
	case "purge":
		exec(db, `DELETE FROM issues WHERE repo = '`+os.Args[3]+`'`)
	}
}

func exec(db *sql.DB, q string) {
	if _, err := db.Exec(q); err != nil {
		panic(err)
	}
	fmt.Println("ok")
}

func list(db *sql.DB, q string) {
	rows, err := db.Query(q)
	if err != nil {
		panic(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			panic(err)
		}
		state := "live"
		if b != "" {
			state = "TOMBSTONED " + b
		}
		fmt.Printf("  %-14s %s\n", a, state)
	}
}
