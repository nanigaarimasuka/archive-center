package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

// TestMariaDBTurnEditAndReplayOnRealSchema runs the edited-turn cleanup and
// the replay cleanup against real MariaDB. It creates its own database, and
// drops it afterwards, so the DSN must name none. The name starts with
// "test_" so an account limited to test databases can run it.
func TestMariaDBTurnEditAndReplayOnRealSchema(t *testing.T) {
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
	name := fmt.Sprintf("test_ac_turn_edit_%d", time.Now().UnixNano())
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
INSERT INTO chat_logs (chat_session_id, turn_index, role, content) VALUES
 ('s1',1,'user','u1'),('s1',1,'assistant','a1'),('s1',2,'user','u2'),('s1',2,'assistant','a2'),
 ('s1',3,'user','u3'),('s1',3,'assistant','a3'),('s1',4,'user','u4'),('s1',4,'assistant','a4');
INSERT INTO memory_source_revisions (source_revision,chat_session_id,logical_turn_id,turn_index,raw_user_content,raw_assistant_content,combined_content_hash,hash_algorithm,host_observed_at_ms) VALUES
 ('rev-2','s1','turn-2',2,'u2','a2',REPEAT('a',64),'sha256',1),
 ('rev-3','s1','turn-3',3,'u3','a3',REPEAT('b',64),'sha256',1);
INSERT INTO pending_threads (id, chat_session_id, thread_key, description, created_turn, source_turn, pinned) VALUES
 (1,'s1','own','lunch only here',2,2,0),
 (2,'s1','shared','lunch then dinner',2,3,0),
 (3,'s1','earlier','duel',1,2,0),
 (4,'s1','pinned','kept by the user',2,2,1);
INSERT INTO storylines (id, chat_session_id, name, first_turn, last_turn) VALUES
 (1,'s1','own arc',2,2),(2,'s1','shared arc',2,4);
INSERT INTO entities (id, chat_session_id, name, first_seen_turn, last_seen_turn) VALUES
 (1,'s1','only in two',2,2),(2,'s1','recurring',1,3);
INSERT INTO trust_states (id, chat_session_id, target_name, source_turn) VALUES
 (1,'s1','rina',2),(2,'s1','rina',3);
INSERT INTO world_rules (id, chat_session_id, scope, category, ` + "`key`" + `, source_turn, pinned) VALUES
 (1,'s1','world','magic','pinned rule',2,1),(2,'s1','world','magic','plain rule',2,0);
INSERT INTO status_schema_registry (id,chat_session_id,status_key,label,owner_scope,value_kind) VALUES
 (7,'s1','injury','Injury','fictional_entity','text'),
 (8,'s1','mood','Mood','fictional_entity','text'),
 (9,'s1','legacy','Legacy','fictional_entity','text');
INSERT INTO status_change_events (id,chat_session_id,registry_id,status_key,owner_scope,owner_id,event_kind,new_value_json,evidence_json,source_turn) VALUES
 (21,'s1',7,'injury','fictional_entity','rina','set','"arm"','{"source_revision":"rev-2","current_projection":true}',2),
 (22,'s1',7,'injury','fictional_entity','rina','set','"healed"','{"source_revision":"rev-3","current_projection":true}',3),
 (23,'s1',8,'mood','fictional_entity','rina','set','"calm"','{"source_revision":"rev-2","current_projection":true}',2),
 (24,'s1',9,'legacy','fictional_entity','rina','set','"old"','{}',2);
INSERT INTO status_current_values (chat_session_id,registry_id,status_key,owner_scope,owner_id,value_kind,value_json,evidence_json,source_turn) VALUES
 ('s1',7,'injury','fictional_entity','rina','text','"healed"','{"source_revision":"rev-3","current_projection":true}',3),
 ('s1',8,'mood','fictional_entity','rina','text','"calm"','{"source_revision":"rev-2","current_projection":true}',2);`
	if _, err := db.ExecContext(ctx, seed); err != nil {
		t.Fatal(err)
	}
	m := &mariadbStore{db: db}
	ids := func(query string) []int64 {
		t.Helper()
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := []int64{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		return out
	}
	expectIDs := func(label, query string, want ...int64) {
		t.Helper()
		if got := ids(query); !reflect.DeepEqual(got, append([]int64{}, want...)) {
			t.Errorf("%s = %v, want %v", label, got, want)
		}
	}

	review, err := m.ReviseHistoricalTurn(ctx, HistoricalTurnEdit{ChatSessionID: "s1", TurnIndex: 2, UserContent: "u2 edited", AssistantContent: "a2 edited", EditedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("ReviseHistoricalTurn: %v", err)
	}
	wantReview := []HistoricalTurnReviewItem{
		{Kind: "pending_thread", ID: 2, Label: "lunch then dinner", TurnIndex: 2, OtherTurn: 3, Reason: "changed_later"},
		{Kind: "pending_thread", ID: 3, Label: "duel", TurnIndex: 2, OtherTurn: 1, Reason: "last_changed_here"},
		{Kind: "storyline", ID: 2, Label: "shared arc", TurnIndex: 2, OtherTurn: 4, Reason: "changed_later"},
		{Kind: "status", ID: 21, Label: "rina · injury", TurnIndex: 2, OtherTurn: 3, Reason: "changed_later"},
	}
	if !reflect.DeepEqual(review, wantReview) {
		t.Errorf("review = %+v\nwant %+v", review, wantReview)
	}
	var edited string
	if err := db.QueryRowContext(ctx, `SELECT content FROM chat_logs WHERE chat_session_id='s1' AND turn_index=2 AND role='assistant'`).Scan(&edited); err != nil || edited != "a2 edited" {
		t.Errorf("turn 2 text = %q (%v)", edited, err)
	}
	expectIDs("threads after edit", `SELECT id FROM pending_threads ORDER BY id`, 2, 3, 4)
	expectIDs("storylines after edit", `SELECT id FROM storylines ORDER BY id`, 2)
	expectIDs("entities after edit", `SELECT id FROM entities ORDER BY id`, 2)
	expectIDs("trust after edit", `SELECT id FROM trust_states ORDER BY id`, 2)
	expectIDs("world rules after edit", `SELECT id FROM world_rules ORDER BY id`, 1)
	expectIDs("status events after edit", `SELECT id FROM status_change_events ORDER BY id`, 21, 22, 23)
	expectIDs("status values after edit", `SELECT registry_id FROM status_current_values ORDER BY registry_id`, 7)

	if err := m.ClearDerivedFromTurn(ctx, "s1", 3, time.Now().UTC()); err != nil {
		t.Fatalf("ClearDerivedFromTurn: %v", err)
	}
	expectIDs("raw turns after replay clear", `SELECT turn_index FROM chat_logs ORDER BY turn_index, role`, 1, 1, 2, 2, 3, 3, 4, 4)
	expectIDs("threads after replay clear", `SELECT id FROM pending_threads ORDER BY id`, 3, 4)
	expectIDs("storylines after replay clear", `SELECT id FROM storylines ORDER BY id`)
	expectIDs("entities after replay clear", `SELECT id FROM entities ORDER BY id`, 2)
	expectIDs("entity last seen after replay clear", `SELECT last_seen_turn FROM entities ORDER BY id`, 2)
	expectIDs("trust after replay clear", `SELECT id FROM trust_states ORDER BY id`)
	expectIDs("world rules after replay clear", `SELECT id FROM world_rules ORDER BY id`, 1)
	expectIDs("status values after replay clear", `SELECT registry_id FROM status_current_values ORDER BY registry_id`)
	expectIDs("active revisions after replay clear", `SELECT turn_index FROM memory_source_revisions WHERE lifecycle_state = 'active' ORDER BY turn_index`)
}
