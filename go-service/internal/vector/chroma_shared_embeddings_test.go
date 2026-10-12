package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// sharedEmbeddingChroma is a small Chroma with exact float literals, so the
// two search paths can be compared on what a real server would send.
type sharedEmbeddingChroma struct {
	mu   sync.Mutex
	docs map[string]*sharedEmbeddingChromaDoc
	// faults for /get
	getStatus int
	getDrop   bool
	getNull   bool
	gets      int
}

type sharedEmbeddingChromaDoc struct {
	id       string
	literals []string
	values   []float64
	text     string
	meta     map[string]any
}

func (c *sharedEmbeddingChroma) serve(w http.ResponseWriter, r *http.Request) {
	const base = "/api/v2/tenants/default_tenant/databases/default_database/collections/"
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	c.mu.Lock()
	defer c.mu.Unlock()
	switch r.URL.Path {
	case base + "archive_center_vectors":
		_, _ = w.Write([]byte(`{"id":"c1","name":"archive_center_vectors"}`))
	case base + "c1/query":
		c.query(w, body)
	case base + "c1/get":
		c.get(w, body)
	case base + "c1/upsert":
		ids := body["ids"].([]any)
		embeddings := body["embeddings"].([]any)
		for i, id := range ids {
			doc := c.docs[id.(string)]
			doc.literals, doc.values = nil, nil
			for _, v := range embeddings[i].([]any) {
				doc.literals = append(doc.literals, strconv.FormatFloat(v.(float64), 'g', -1, 32))
				doc.values = append(doc.values, v.(float64))
			}
		}
		_, _ = w.Write([]byte(`{}`))
	default:
		http.Error(w, r.URL.Path, http.StatusNotFound)
	}
}

func (c *sharedEmbeddingChroma) matches(doc *sharedEmbeddingChromaDoc, where any) bool {
	clauses := []any{where}
	if m, ok := where.(map[string]any); ok {
		if and, ok := m["$and"].([]any); ok {
			clauses = and
		}
	}
	for _, clause := range clauses {
		m, _ := clause.(map[string]any)
		for key, value := range m {
			if eq, ok := value.(map[string]any); ok {
				value = eq["$eq"]
			}
			if fmt.Sprint(doc.meta[key]) != fmt.Sprint(value) {
				return false
			}
		}
	}
	return true
}

func (c *sharedEmbeddingChroma) query(w http.ResponseWriter, body map[string]any) {
	query := body["query_embeddings"].([]any)[0].([]any)
	type hit struct {
		doc      *sharedEmbeddingChromaDoc
		distance float64
	}
	var hits []hit
	for _, doc := range c.docs {
		if body["where"] != nil && !c.matches(doc, body["where"]) {
			continue
		}
		distance := 0.0
		for i, v := range query {
			if i < len(doc.values) {
				d := v.(float64) - doc.values[i]
				distance += d * d
			}
		}
		hits = append(hits, hit{doc, distance})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].distance != hits[j].distance {
			return hits[i].distance < hits[j].distance
		}
		return hits[i].doc.id < hits[j].doc.id
	})
	if n := int(body["n_results"].(float64)); len(hits) > n {
		hits = hits[:n]
	}
	include := map[string]bool{}
	for _, v := range body["include"].([]any) {
		include[v.(string)] = true
	}
	var ids, docs, metas, distances, embeddings []string
	for _, h := range hits {
		ids = append(ids, strconv.Quote(h.doc.id))
		text, _ := json.Marshal(h.doc.text)
		docs = append(docs, string(text))
		meta, _ := json.Marshal(h.doc.meta)
		metas = append(metas, string(meta))
		distances = append(distances, strconv.FormatFloat(h.distance, 'g', -1, 64))
		embeddings = append(embeddings, "["+strings.Join(h.doc.literals, ",")+"]")
	}
	parts := []string{`"ids":[[` + strings.Join(ids, ",") + `]]`}
	for name, values := range map[string][]string{"documents": docs, "metadatas": metas, "distances": distances, "embeddings": embeddings} {
		if include[name] {
			parts = append(parts, `"`+name+`":[[`+strings.Join(values, ",")+`]]`)
		}
	}
	_, _ = w.Write([]byte("{" + strings.Join(parts, ",") + "}"))
}

func (c *sharedEmbeddingChroma) get(w http.ResponseWriter, body map[string]any) {
	c.gets++
	if c.getStatus != 0 {
		http.Error(w, "boom", c.getStatus)
		return
	}
	var ids, embeddings []string
	requested := body["ids"].([]any)
	// Chroma does not keep the requested order.
	for i := len(requested) - 1; i >= 0; i-- {
		doc := c.docs[requested[i].(string)]
		if doc == nil || (c.getDrop && i == 0) {
			continue
		}
		ids = append(ids, strconv.Quote(doc.id))
		if c.getNull && i == 0 {
			embeddings = append(embeddings, "null")
		} else {
			embeddings = append(embeddings, "["+strings.Join(doc.literals, ",")+"]")
		}
	}
	_, _ = w.Write([]byte(`{"ids":[` + strings.Join(ids, ",") + `],"embeddings":[` + strings.Join(embeddings, ",") + `]}`))
}

func newSharedEmbeddingChroma(r *rand.Rand, docs, dim int) *sharedEmbeddingChroma {
	c := &sharedEmbeddingChroma{docs: map[string]*sharedEmbeddingChromaDoc{}}
	for i := 0; i < docs; i++ {
		doc := &sharedEmbeddingChromaDoc{
			id:   fmt.Sprintf("doc-%d", i),
			text: fmt.Sprintf("text %d", i),
			meta: map[string]any{
				"chat_session_id": fmt.Sprintf("s%d", r.Intn(2)),
				"tier":            []string{"memory", "turn"}[r.Intn(2)],
				"source_table":    []string{"memories", "precise_memory_units"}[r.Intn(2)],
			},
		}
		n := dim
		if r.Intn(20) == 0 {
			n = 0 // stored without an embedding
		}
		for j := 0; j < n; j++ {
			literal := strconv.FormatFloat((r.Float64()-0.5)*2, 'g', -1, 32)
			switch r.Intn(15) {
			case 0:
				literal = fmt.Sprintf("%.3e", r.Float64())
			case 1:
				literal = "-0"
			}
			value, _ := strconv.ParseFloat(literal, 64)
			doc.literals = append(doc.literals, literal)
			doc.values = append(doc.values, value)
		}
		c.docs[doc.id] = doc
	}
	return c
}

type sharedEmbeddingSearch struct {
	session string
	vector  []float32
	limit   int
	filter  string
}

func runSharedEmbeddingSearches(ctx context.Context, store VectorStore, searches []sharedEmbeddingSearch) ([][]VectorDocument, []string) {
	results := make([][]VectorDocument, len(searches))
	errs := make([]string, len(searches))
	var wg sync.WaitGroup
	for i, search := range searches {
		wg.Add(1)
		go func(i int, search sharedEmbeddingSearch) {
			defer wg.Done()
			docs, err := store.Search(ctx, search.session, search.vector, search.limit, search.filter)
			results[i] = docs
			if err != nil {
				errs[i] = err.Error()
			}
		}(i, search)
	}
	wg.Wait()
	return results, errs
}

// Searches that share embeddings return exactly what each full query returns,
// including after a mutation and when /get misbehaves, and report the same
// query responses.
func TestSharedEmbeddingSearchMatchesFullQueries(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	for round := 0; round < 60; round++ {
		const dim = 6
		chroma := newSharedEmbeddingChroma(r, 5+r.Intn(40), dim)
		server := httptest.NewServer(http.HandlerFunc(chroma.serve))
		raw, err := NewChromaStore(server.URL, "archive_center_vectors", "/api/v2")
		if err != nil {
			t.Fatal(err)
		}
		store := NewMutationFencedStore(raw)
		switch round % 6 {
		case 1:
			chroma.getStatus = http.StatusInternalServerError
		case 2:
			chroma.getDrop = true
		case 3:
			chroma.getNull = true
		}
		var observed [2]map[int]int
		shared := WithSharedEmbeddings(context.Background())
		for step := 0; step < 3; step++ {
			var searches []sharedEmbeddingSearch
			for i := 0; i < 1+r.Intn(10); i++ {
				vector := make([]float32, dim)
				for j := range vector {
					vector[j] = float32(r.Float64() - 0.5)
				}
				filter := []string{"", `tier == "memory"`, `source_table == "precise_memory_units"`}[r.Intn(3)]
				searches = append(searches, sharedEmbeddingSearch{fmt.Sprintf("s%d", r.Intn(2)), vector, 1 + r.Intn(30), filter})
			}
			var got, want [][]VectorDocument
			var gotErrs, wantErrs []string
			for path, ctx := range []context.Context{context.Background(), shared} {
				if observed[path] == nil {
					observed[path] = map[int]int{}
				}
				counts := observed[path]
				var mu sync.Mutex
				ctx = WithQueryResponseObserver(ctx, func(_ int, status int) {
					mu.Lock()
					counts[status]++
					mu.Unlock()
				})
				results, errs := runSharedEmbeddingSearches(ctx, store, searches)
				if path == 0 {
					want, wantErrs = results, errs
				} else {
					got, gotErrs = results, errs
				}
			}
			if !reflect.DeepEqual(gotErrs, wantErrs) {
				t.Fatalf("round %d step %d: errors %v, want %v", round, step, gotErrs, wantErrs)
			}
			for i := range want {
				if !sameVectorDocuments(got[i], want[i]) {
					t.Fatalf("round %d step %d search %d:\n got %#v\nwant %#v", round, step, i, got[i], want[i])
				}
			}
			// A stored embedding changes between searches of the same context.
			var ids []string
			for id := range chroma.docs {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			doc := chroma.docs[ids[r.Intn(len(ids))]]
			changed := make([]float32, dim)
			for j := range changed {
				changed[j] = float32(r.Float64() - 0.5)
			}
			if err := store.Upsert(context.Background(), fmt.Sprint(doc.meta["chat_session_id"]), []VectorDocument{{ID: doc.id, Embedding: changed, DocumentText: doc.text}}); err != nil {
				t.Fatal(err)
			}
		}
		if !reflect.DeepEqual(observed[0], observed[1]) {
			t.Fatalf("round %d: query responses %v, want %v", round, observed[1], observed[0])
		}
		if round%6 == 0 && chroma.gets == 0 {
			t.Fatalf("round %d: embeddings were never shared", round)
		}
		server.Close()
	}
}

func sameVectorDocuments(a, b []VectorDocument) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if len(x.Embedding) != len(y.Embedding) || (x.Embedding == nil) != (y.Embedding == nil) {
			return false
		}
		for j := range x.Embedding {
			if math.Float32bits(x.Embedding[j]) != math.Float32bits(y.Embedding[j]) {
				return false
			}
		}
		x.Embedding, y.Embedding = nil, nil
		if math.Float64bits(x.Similarity) != math.Float64bits(y.Similarity) || math.Float64bits(x.Distance) != math.Float64bits(y.Distance) {
			return false
		}
		if !reflect.DeepEqual(x, y) {
			return false
		}
	}
	return true
}

// The /get decoder gives exactly what json.Unmarshal gives.
func TestDecodeChromaGetEmbeddingsResponseMatchesEncodingJSON(t *testing.T) {
	r := rand.New(rand.NewSource(13))
	for c := 0; c < 4000; c++ {
		// A query document with its outer level removed is a get document.
		data := randomChromaQueryJSON(r)
		data = strings.NewReplacer("[[", "[", "]]", "]").Replace(data)
		var want chromaGetEmbeddingsResponse
		wantErr := json.Unmarshal([]byte(data), &want)
		var got chromaGetEmbeddingsResponse
		gotErr := decodeChromaGetEmbeddingsResponse([]byte(data), &got)
		if (wantErr == nil) != (gotErr == nil) || (wantErr != nil && gotErr.Error() != wantErr.Error()) {
			t.Fatalf("case %d: error %v, want %v\n%s", c, gotErr, wantErr, data)
		}
		if wantErr != nil {
			continue
		}
		wrap := func(v chromaGetEmbeddingsResponse) chromaQueryResponse {
			out := chromaQueryResponse{}
			if v.IDs != nil {
				out.IDs = [][]string{v.IDs}
			}
			if v.Embeddings != nil {
				out.Embeddings = [][][]float32{v.Embeddings}
			}
			return out
		}
		if !sameChromaQueryResponse(wrap(got), wrap(want)) {
			t.Fatalf("case %d: decoded %#v, want %#v\n%s", c, got, want, data)
		}
	}
}

// A later request reuses embeddings an earlier one fetched, until a write
// to those documents.
func TestStoredEmbeddingsServeLaterRequests(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	chroma := newSharedEmbeddingChroma(r, 30, 4)
	server := httptest.NewServer(http.HandlerFunc(chroma.serve))
	defer server.Close()
	raw, err := NewChromaStore(server.URL, "archive_center_vectors", "/api/v2")
	if err != nil {
		t.Fatal(err)
	}
	store := NewMutationFencedStore(raw)
	vector := []float32{.1, .2, .3, .4}
	search := func() []VectorDocument {
		docs, err := store.Search(WithSharedEmbeddings(context.Background()), "s0", vector, 30, "")
		if err != nil {
			t.Fatal(err)
		}
		return docs
	}
	first := search()
	gets := chroma.gets
	if again := search(); chroma.gets != gets || !sameVectorDocuments(again, first) {
		t.Fatalf("second request fetched again (%d gets, was %d) or changed results", chroma.gets, gets)
	}
	// Writing one document refetches only it, with its new value.
	target := first[0]
	changed := []float32{.9, .8, .7, .6}
	if err := store.Upsert(context.Background(), "s0", []VectorDocument{{ID: target.ID, Embedding: changed, DocumentText: target.DocumentText}}); err != nil {
		t.Fatal(err)
	}
	after := search()
	if chroma.gets != gets+1 {
		t.Fatalf("gets after one write = %d, want %d", chroma.gets, gets+1)
	}
	want, err := store.Search(context.Background(), "s0", vector, 30, "")
	if err != nil {
		t.Fatal(err)
	}
	if !sameVectorDocuments(after, want) {
		t.Fatal("results after a write differ from a full query")
	}
}

// A write the fence cannot attribute to documents forgets every kept
// embedding.
func TestStoredEmbeddingsForgetAfterExclusiveWrite(t *testing.T) {
	r := rand.New(rand.NewSource(9))
	chroma := newSharedEmbeddingChroma(r, 20, 4)
	server := httptest.NewServer(http.HandlerFunc(chroma.serve))
	defer server.Close()
	raw, err := NewChromaStore(server.URL, "archive_center_vectors", "/api/v2")
	if err != nil {
		t.Fatal(err)
	}
	store := NewMutationFencedStore(raw)
	vector := []float32{.4, .3, .2, .1}
	first, err := store.Search(WithSharedEmbeddings(context.Background()), "s0", vector, 20, "")
	if err != nil || len(first) == 0 {
		t.Fatal(err)
	}
	target := first[0]
	err = store.(MutationFencer).WithExclusiveMutationFence(context.Background(), func(delegate VectorStore) error {
		return delegate.Upsert(context.Background(), "s0", []VectorDocument{{ID: target.ID, Embedding: []float32{.5, .5, .5, .5}, DocumentText: target.DocumentText}})
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Search(WithSharedEmbeddings(context.Background()), "s0", vector, 20, "")
	if err != nil {
		t.Fatal(err)
	}
	want, err := store.Search(context.Background(), "s0", vector, 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if !sameVectorDocuments(got, want) {
		t.Fatal("kept embedding survived an exclusive write")
	}
}

func TestStoredEmbeddingsForget(t *testing.T) {
	stored := newStoredEmbeddings()
	for _, id := range []string{"a", "b", "c"} {
		stored.put(id, []float32{1, 2})
	}
	stored.forget([]VectorDocument{{ID: " a "}})
	if _, ok := stored.get("a"); ok {
		t.Fatal("written id kept")
	}
	if _, ok := stored.get("b"); !ok {
		t.Fatal("unwritten id forgotten")
	}
	// A document without an id may be any document: everything goes.
	stored.forget([]VectorDocument{{ID: "x"}, {ID: " "}})
	for _, id := range []string{"b", "c"} {
		if _, ok := stored.get(id); ok {
			t.Fatalf("id %s kept after a write of unknown documents", id)
		}
	}
	var none *storedEmbeddings
	none.put("a", []float32{1})
	none.forget([]VectorDocument{{ID: ""}})
	if _, ok := none.get("a"); ok {
		t.Fatal("nil cache returned an embedding")
	}
}

// Searches that keep embeddings run alongside writes; afterwards no kept
// embedding differs from what the collection holds.
func TestStoredEmbeddingsStayCurrentUnderConcurrentWrites(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	chroma := newSharedEmbeddingChroma(r, 40, 4)
	server := httptest.NewServer(http.HandlerFunc(chroma.serve))
	defer server.Close()
	raw, err := NewChromaStore(server.URL, "archive_center_vectors", "/api/v2")
	if err != nil {
		t.Fatal(err)
	}
	store := NewMutationFencedStore(raw)
	vector := []float32{.2, .4, .1, .3}
	ids := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		ids = append(ids, fmt.Sprintf("doc-%d", i))
	}
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				if _, err := store.Search(WithSharedEmbeddings(context.Background()), "s0", vector, 40, ""); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			id := ids[(i*7)%len(ids)]
			value := float32(i%10) / 10
			doc := VectorDocument{ID: id, Embedding: []float32{value, 1 - value, value / 2, .5}, DocumentText: "text " + id}
			var err error
			if i%5 == 0 {
				err = store.(MutationFencer).WithExclusiveMutationFence(context.Background(), func(delegate VectorStore) error {
					return delegate.Upsert(context.Background(), "s0", []VectorDocument{doc})
				})
			} else {
				err = store.Upsert(context.Background(), "s0", []VectorDocument{doc})
			}
			if err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()
	got, err := store.Search(WithSharedEmbeddings(context.Background()), "s0", vector, 40, "")
	if err != nil {
		t.Fatal(err)
	}
	want, err := store.Search(context.Background(), "s0", vector, 40, "")
	if err != nil {
		t.Fatal(err)
	}
	if !sameVectorDocuments(got, want) {
		t.Fatal("a kept embedding differs from the collection after concurrent writes")
	}
}
