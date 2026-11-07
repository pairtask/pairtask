package main

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

func offlineSnapshot(t *testing.T, path string) string {
	t.Helper()
	db := offlineSQL(t, path, true)
	result := map[string][]string{}
	for _, table := range offlineTables(t, db) {
		result[table] = offlineTableRows(t, db, table)
	}
	data, err := json.Marshal(result)
	offlineOK(t, err)
	return string(data)
}

func offlineTables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND (name NOT LIKE 'sqlite_%' OR name='sqlite_sequence') ORDER BY name`)
	offlineOK(t, err)
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		offlineOK(t, rows.Scan(&name))
		tables = append(tables, name)
	}
	offlineOK(t, rows.Err())
	return tables
}

func offlineTableRows(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
	rows, err := db.Query("SELECT * FROM " + quoted)
	offlineOK(t, err)
	defer rows.Close()
	columns, err := rows.Columns()
	offlineOK(t, err)
	var result []string
	for rows.Next() {
		data, err := json.Marshal(offlineValues(t, rows, len(columns)))
		offlineOK(t, err)
		result = append(result, string(data))
	}
	offlineOK(t, rows.Err())
	sort.Strings(result)
	return result
}

func offlineValues(t *testing.T, rows *sql.Rows, size int) []any {
	t.Helper()
	values, pointers := make([]any, size), make([]any, size)
	for i := range values {
		pointers[i] = &values[i]
	}
	offlineOK(t, rows.Scan(pointers...))
	return values
}

func offlineCount(t *testing.T, path, query string) int64 {
	t.Helper()
	var result int64
	offlineOK(t, offlineSQL(t, path, true).QueryRow(query).Scan(&result))
	return result
}
