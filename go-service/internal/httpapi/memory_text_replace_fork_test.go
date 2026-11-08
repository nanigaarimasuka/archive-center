package httpapi

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

type memoryTextFakeStore struct {
	store.Store
	rows    []store.MemoryTextRow
	updates []map[string]string
}

func (f *memoryTextFakeStore) ListMemoryTextRows(context.Context, string) ([]store.MemoryTextRow, error) {
	return f.rows, nil
}

func (f *memoryTextFakeStore) UpdateMemoryTextRow(_ context.Context, _, table string, id int64, before, after map[string]string) (bool, error) {
	for i := range f.rows {
		row := &f.rows[i]
		if row.Table != table || row.ID != id {
			continue
		}
		for column, value := range before {
			if row.Values[column] != value {
				return false, nil
			}
		}
		for column, value := range after {
			if column == "derived_result_hash" {
				row.Extra[column] = value
				continue
			}
			row.Values[column] = value
		}
		f.updates = append(f.updates, after)
		return true, nil
	}
	return false, nil
}

func TestMemoryTextFieldsSkipIdentityKeys(t *testing.T) {
	rows := []store.MemoryTextRow{{
		Table: "memories", ID: 4, Turn: 9,
		Values: map[string]string{"summary_json": `{"turn_summary":"Mira waves","state_claims":[{"subject":"Mira","state_slot":"mood","lifecycle_key":"mira-mood","value":"Mira is calm"}],"character_deltas":[{"name":"Mira","note":"Mira smiles"}],"importance_score":5}`},
	}, {
		Table: "kg_triples", ID: 2, Turn: 3, Values: map[string]string{"subject": "Mira", "predicate": "knows", "object": "Rook"},
	}}
	got := map[string]string{}
	for _, field := range memoryTextFields(rows) {
		got[field.Ref] = field.Text
	}
	want := map[string]string{
		"memories|4|summary_json|/character_deltas/0/note": "Mira smiles",
		"memories|4|summary_json|/state_claims/0/value":    "Mira is calm",
		"memories|4|summary_json|/turn_summary":            "Mira waves",
		"kg_triples|2|subject|":                            "Mira",
		"kg_triples|2|predicate|":                          "knows",
		"kg_triples|2|object|":                             "Rook",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fields = %v", got)
	}
}

func TestMemoryTextApplyRewritesTheStoredResultWithItsHash(t *testing.T) {
	result := `{"turn_summary":"Mira waves","importance_score":5}`
	fake := &memoryTextFakeStore{rows: []store.MemoryTextRow{
		{Table: store.MemoryTextSourceTable, ID: 1, Turn: 7,
			Values: map[string]string{"raw_user_content": "hi Mira", "raw_assistant_content": "Mira waves", "derived_result_json": result},
			Extra:  map[string]string{"source_revision": "sar_1", "derived_admission_version": "a1", "derived_extractor_version": "e1", "derived_index_version": "i1"}},
		{Table: "world_rules", ID: 5, Turn: 2, Values: map[string]string{"value_json": `{"text":"Mira rules the harbor","category":"Mira"}`}},
		{Table: "kg_triples", ID: 8, Turn: 3, Values: map[string]string{"subject": "Mira", "predicate": "knows", "object": "Rook"}},
	}}
	s := &Server{}
	out, err := s.applyMemoryTextChanges(context.Background(), "sid", fake, []memoryTextChange{
		{Ref: "memory_source_revisions|1|raw_user_content|", Before: "hi Mira", After: "hi Mina"},
		{Ref: "memory_source_revisions|1|derived_result_json|/turn_summary", Before: "Mira waves", After: "Mina waves"},
		{Ref: "world_rules|5|value_json|/text", Before: "Mira rules the harbor", After: "Mina rules the harbor"},
		{Ref: "world_rules|5|value_json|/category", Before: "Mira", After: "Mina"}, // an identity key: refused
		{Ref: "kg_triples|8|subject|", Before: "Mia", After: "Mina"},               // read earlier, changed since
		{Ref: "users|1|password|", Before: "x", After: "y"},                        // not in the catalog
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["applied"] != 3 || len(out["stale"].([]string)) != 3 {
		t.Fatalf("result = %+v", out)
	}
	if !reflect.DeepEqual(out["changed_turns"], []int{7}) || !reflect.DeepEqual(out["changed_world_rules"], []int64{5}) {
		t.Fatalf("changed = %v %v", out["changed_turns"], out["changed_world_rules"])
	}
	source := fake.rows[0]
	if source.Values["raw_user_content"] != "hi Mina" || !strings.Contains(source.Values["derived_result_json"], `"turn_summary":"Mina waves"`) {
		t.Fatalf("source = %+v", source.Values)
	}
	// Replay decodes the stored result, re-encodes it and checks this hash.
	var decoded map[string]any
	if err := json.Unmarshal([]byte(source.Values["derived_result_json"]), &decoded); err != nil {
		t.Fatal(err)
	}
	if mustCompactJSON(decoded) != source.Values["derived_result_json"] ||
		source.Extra["derived_result_hash"] != memoryAdmissionResultHashFromCanonicalJSON("sar_1", mustCompactJSON(decoded), "a1", "e1", "i1") {
		t.Fatalf("stored result and hash do not match what replay checks: %+v", source.Extra)
	}
	if !strings.Contains(fake.rows[1].Values["value_json"], `"category":"Mira"`) || fake.rows[2].Values["subject"] != "Mira" {
		t.Fatalf("refused changes were written: %+v %+v", fake.rows[1].Values, fake.rows[2].Values)
	}
}

func TestSetMemoryTextNeedsTheValueItRead(t *testing.T) {
	var value any
	_ = json.Unmarshal([]byte(`{"a":[{"b~/c":"old"}]}`), &value)
	if setMemoryText(value, "/a/0/b~0~1c", "other", "new") {
		t.Fatal("changed a value that no longer reads before")
	}
	if !setMemoryText(value, "/a/0/b~0~1c", "old", "new") || mustCompactJSON(value) != `{"a":[{"b~/c":"new"}]}` {
		t.Fatalf("value = %s", mustCompactJSON(value))
	}
	if setMemoryText(value, "/a/9/x", "old", "new") || setMemoryText(value, "/a/0", "old", "new") {
		t.Fatal("a bad pointer changed something")
	}
}

type memoryTextIdentityStore struct {
	store.Store
	identities []store.EntityIdentity
	links      []store.EntityIdentityLink
	surfaces   []store.EntityIdentitySurface
	labels     map[string]string
}

func (f *memoryTextIdentityStore) ListActiveEntityIdentities(context.Context, string) ([]store.EntityIdentity, error) {
	return f.identities, nil
}
func (f *memoryTextIdentityStore) ListActiveEntityIdentitySurfaces(context.Context, string) ([]store.EntityIdentitySurface, error) {
	return nil, nil
}
func (f *memoryTextIdentityStore) ListReviewedEntityIdentityLinks(context.Context, string) ([]store.EntityIdentityLink, error) {
	return nil, nil
}
func (f *memoryTextIdentityStore) SaveEntityIdentityLink(_ context.Context, link *store.EntityIdentityLink) error {
	f.links = append(f.links, *link)
	return nil
}
func (f *memoryTextIdentityStore) SaveEntityIdentitySurface(_ context.Context, surface *store.EntityIdentitySurface) error {
	f.surfaces = append(f.surfaces, *surface)
	return nil
}
func (f *memoryTextIdentityStore) SetEntityIdentityLabel(_ context.Context, _, id, label string) error {
	f.labels[id] = label
	return nil
}

func TestMemoryTextRenamesMergeIntoAnExistingNameOrRename(t *testing.T) {
	fake := &memoryTextIdentityStore{labels: map[string]string{}, identities: []store.EntityIdentity{
		{StableEntityID: "mira", ChatSessionID: "sid", EntityKind: "character", CanonicalLabel: "Mira", IdentityNamespace: "session_npc", LifecycleState: "active"},
		{StableEntityID: "rook", ChatSessionID: "sid", EntityKind: "character", CanonicalLabel: "Rook", IdentityNamespace: "session_npc", LifecycleState: "active"},
		{StableEntityID: "rooky", ChatSessionID: "sid", EntityKind: "character", CanonicalLabel: "Rooky", IdentityNamespace: "session_npc", LifecycleState: "active"},
	}}
	s := &Server{Store: fake}
	if _, err := s.characterIdentityCatalogForSession(context.Background(), "sid"); err != nil {
		t.Fatal(err)
	}
	results := s.applyMemoryTextRenames(context.Background(), "sid", []memoryTextRename{
		{StableEntityID: "mira", To: "Mina"}, {StableEntityID: "rooky", To: "Rook"}, {StableEntityID: "ghost", To: "X"},
	})
	if len(results) != 3 || results[0]["status"] != "renamed" || results[1]["status"] != "merged" || results[2]["status"] != "failed" {
		t.Fatalf("results = %+v", results)
	}
	if fake.labels["mira"] != "Mina" || len(fake.surfaces) != 1 || fake.surfaces[0].SurfaceText != "Mina" || fake.surfaces[0].StableEntityID != "mira" {
		t.Fatalf("rename wrote labels %v surfaces %+v", fake.labels, fake.surfaces)
	}
	if len(fake.links) != 1 || fake.links[0].SourceEntityID != "rooky" || fake.links[0].TargetEntityID != "rook" {
		t.Fatalf("merge wrote links %+v", fake.links)
	}
}
