package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

// TestMariaDBMemoryTextOnRealSchema reads every catalog table and writes a
// few of them against real MariaDB, with the same throwaway database as the
// turn edit test.
func TestMariaDBMemoryTextOnRealSchema(t *testing.T) {
	dsn := os.Getenv("AC_TURN_EDIT_TEST_DSN")
	if dsn == "" {
		t.Skip("set AC_TURN_EDIT_TEST_DSN to a MariaDB account that may create databases")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBName != "" {
		t.Fatal("AC_TURN_EDIT_TEST_DSN must not name a database; the test makes its own")
	}
	cfg.ParseTime, cfg.MultiStatements = true, true
	adminConnector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	admin := sql.OpenDB(adminConnector)
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	name := fmt.Sprintf("test_ac_memory_text_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name+" CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec("DROP DATABASE " + name); err != nil {
			t.Errorf("cleanup database: %v", err)
		}
	}()
	cfg.DBName = name
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	schema, err := os.ReadFile("../../../migrations/001_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(schema)); err != nil {
		t.Fatal(err)
	}
	seed := `
INSERT INTO memory_source_revisions (source_revision,chat_session_id,logical_turn_id,turn_index,raw_user_content,raw_assistant_content,combined_content_hash,hash_algorithm,host_observed_at_ms,
  derived_admission_state,derived_admission_version,derived_extractor_version,derived_index_version,derived_result_hash,derived_result_json) VALUES
 ('rev-2','s1','turn-2',2,'hi Mira','Mira waves',REPEAT('a',64),'sha256',1,'committed','a1','e1','i1','old-hash','{"turn_summary":"Mira waves"}'),
 ('rev-3','s1','turn-3',3,'u3','a3',REPEAT('b',64),'sha256',1,'pending','a1','e1','i1','',NULL);
INSERT INTO kg_triples (id, chat_session_id, subject, predicate, object, source_turn) VALUES (7,'s1','Mira','knows','Rook',2);
`
	if _, err := db.ExecContext(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO world_rules (id, chat_session_id, scope, scope_name, category, `key`, value_json, source_turn) VALUES (5,'s1','global','','lore','harbor','{\"text\":\"Mira rules the harbor\"}',2)"); err != nil {
		t.Fatal(err)
	}
	m := &mariadbStore{db: db}
	rows, err := m.ListMemoryTextRows(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	byTable := map[string][]MemoryTextRow{}
	for _, row := range rows {
		byTable[row.Table] = append(byTable[row.Table], row)
	}
	// Only the committed, active source revision is read.
	sources := byTable[MemoryTextSourceTable]
	if len(sources) != 1 || sources[0].Turn != 2 || sources[0].Values["raw_user_content"] != "hi Mira" || sources[0].Extra["derived_index_version"] != "i1" {
		t.Fatalf("source rows = %+v", sources)
	}
	if len(byTable["kg_triples"]) != 1 || byTable["kg_triples"][0].Values["subject"] != "Mira" || len(byTable["world_rules"]) != 1 {
		t.Fatalf("rows = %+v", byTable)
	}

	written, err := m.UpdateMemoryTextRow(ctx, "s1", MemoryTextSourceTable, sources[0].ID,
		map[string]string{"raw_user_content": "hi Mira", "derived_result_json": `{"turn_summary":"Mira waves"}`},
		map[string]string{"raw_user_content": "hi Mina", "derived_result_json": `{"turn_summary":"Mina waves"}`, "derived_result_hash": "new-hash"})
	if err != nil || !written {
		t.Fatalf("source write: %v %v", written, err)
	}
	var user, result, hash string
	if err := db.QueryRowContext(ctx, `SELECT raw_user_content, derived_result_json, derived_result_hash FROM memory_source_revisions WHERE source_revision='rev-2'`).Scan(&user, &result, &hash); err != nil {
		t.Fatal(err)
	}
	if user != "hi Mina" || !strings.Contains(result, "Mina waves") || hash != "new-hash" {
		t.Fatalf("stored %q %q %q", user, result, hash)
	}
	// A value changed since it was read is not overwritten.
	if written, err := m.UpdateMemoryTextRow(ctx, "s1", "kg_triples", 7, map[string]string{"subject": "Mia"}, map[string]string{"subject": "Mina"}); err != nil || written {
		t.Fatalf("stale write: %v %v", written, err)
	}
	if written, err := m.UpdateMemoryTextRow(ctx, "s1", "kg_triples", 7, map[string]string{"subject": "Mira"}, map[string]string{"subject": "Mina"}); err != nil || !written {
		t.Fatalf("kg write: %v %v", written, err)
	}
	// Columns outside the catalog are refused before any SQL runs.
	if _, err := m.UpdateMemoryTextRow(ctx, "s1", "kg_triples", 7, nil, map[string]string{"chat_session_id": "other"}); err == nil {
		t.Fatal("wrote a column outside the catalog")
	}
	if err := m.SetEntityIdentityLabel(ctx, "s1", "missing", "Mina"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("label of a missing identity: %v", err)
	}
}
