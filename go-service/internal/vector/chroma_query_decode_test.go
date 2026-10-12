package vector

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The fast path must give exactly what json.Unmarshal gives, including nil
// versus empty slices, -0 and whether decoding fails.
func TestDecodeChromaQueryResponseMatchesEncodingJSON(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	for c := 0; c < 4000; c++ {
		data := randomChromaQueryJSON(r)
		var want chromaQueryResponse
		wantErr := json.Unmarshal([]byte(data), &want)
		var got chromaQueryResponse
		gotErr := decodeChromaQueryResponse([]byte(data), &got)
		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("case %d: error %v, want %v\n%s", c, gotErr, wantErr, data)
		}
		if wantErr != nil {
			if gotErr.Error() != wantErr.Error() {
				t.Fatalf("case %d: error %q, want %q", c, gotErr, wantErr)
			}
			continue
		}
		if !sameChromaQueryResponse(got, want) {
			t.Fatalf("case %d: decoded %#v, want %#v\n%s", c, got, want, data)
		}
	}
}

func sameChromaQueryResponse(a, b chromaQueryResponse) bool {
	if !reflect.DeepEqual(a.IDs, b.IDs) || !reflect.DeepEqual(a.Documents, b.Documents) ||
		!reflect.DeepEqual(a.Metadatas, b.Metadatas) || !reflect.DeepEqual(a.Distances, b.Distances) {
		return false
	}
	if (a.Embeddings == nil) != (b.Embeddings == nil) || len(a.Embeddings) != len(b.Embeddings) {
		return false
	}
	for i := range a.Embeddings {
		if (a.Embeddings[i] == nil) != (b.Embeddings[i] == nil) || len(a.Embeddings[i]) != len(b.Embeddings[i]) {
			return false
		}
		for j := range a.Embeddings[i] {
			x, y := a.Embeddings[i][j], b.Embeddings[i][j]
			if (x == nil) != (y == nil) || len(x) != len(y) {
				return false
			}
			for k := range x {
				if math.Float32bits(x[k]) != math.Float32bits(y[k]) {
					return false
				}
			}
		}
	}
	return true
}

func randomChromaQueryJSON(r *rand.Rand) string {
	space := func() string { return []string{"", "", " ", "\n  ", "\t"}[r.Intn(5)] }
	number := func() string {
		switch r.Intn(10) {
		case 0:
			return strconv.Itoa(r.Intn(2000) - 1000)
		case 1:
			return "-0"
		case 2:
			return fmt.Sprintf("%.3e", (r.Float64()-0.5)*math.Pow(10, float64(r.Intn(80)-40)))
		case 3:
			return fmt.Sprintf("%gE%+d", r.Float64(), r.Intn(20)-10)
		case 4:
			return "1e39" // out of float32 range: encoding/json fails
		case 5:
			return []string{"01", "1.", ".5", "+1", "1e", "-", "NaN"}[r.Intn(7)] // invalid JSON numbers
		default:
			return strconv.FormatFloat((r.Float64()-0.5)*2, 'f', -1, 64)
		}
	}
	arr := func(n int, elem func() string) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = space() + elem() + space()
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	valid := r.Intn(4) != 0
	numberOrValid := func() string {
		if valid {
			return strconv.FormatFloat((r.Float64()-0.5)*2, 'g', -1, 32)
		}
		return number()
	}
	rows := r.Intn(4)
	embeddings := arr(1, func() string {
		return arr(rows, func() string {
			if r.Intn(30) == 0 {
				return "null"
			}
			return arr(r.Intn(5), numberOrValid)
		})
	})
	switch r.Intn(12) {
	case 0:
		embeddings = "null"
	case 1:
		embeddings = "[]"
	case 2:
		embeddings = "[[]]"
	case 3:
		embeddings = `{"a":1}`
	}
	fields := []string{
		`"ids":` + arr(1, func() string { return arr(rows, func() string { return strconv.Quote(fmt.Sprint("id", r.Intn(9))) }) }),
		`"documents":` + arr(1, func() string { return arr(rows, func() string { return `"d\"[x]\n` + fmt.Sprint(r.Intn(9)) + `"` }) }),
		`"metadatas":` + arr(1, func() string { return arr(rows, func() string { return `{"k":[1,{"n":null}],"s":"}]"}` }) }),
		`"distances":` + arr(1, func() string {
			return arr(rows, func() string { return strconv.FormatFloat(r.Float64(), 'g', -1, 64) })
		}),
	}
	embeddingKey := `"embeddings"`
	switch r.Intn(10) {
	case 0:
		embeddingKey = `"Embeddings"`
	case 1:
		embeddingKey = `"embeddings"`
	}
	if r.Intn(8) != 0 {
		fields = append(fields, embeddingKey+space()+":"+space()+embeddings)
	}
	if r.Intn(15) == 0 {
		fields = append(fields, `"embeddings":[[[0.5]]]`) // duplicate key: last wins
	}
	r.Shuffle(len(fields), func(i, j int) { fields[i], fields[j] = fields[j], fields[i] })
	doc := "{" + space() + strings.Join(fields, ","+space()) + space() + "}"
	switch r.Intn(25) {
	case 0:
		doc += " x" // trailing data
	case 1:
		doc = doc[:len(doc)/2] // truncated
	}
	return space() + doc + space()
}

func BenchmarkDecodeChromaQueryResponse(b *testing.B) {
	r := rand.New(rand.NewSource(3))
	var sb strings.Builder
	sb.WriteString(`{"ids":[[`)
	const rows, dim = 500, 1536
	for i := 0; i < rows; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `"id%d"`, i)
	}
	sb.WriteString(`]],"distances":[[`)
	for i := 0; i < rows; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatFloat(r.Float64(), 'g', -1, 64))
	}
	sb.WriteString(`]],"embeddings":[[`)
	for i := 0; i < rows; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteByte('[')
		for j := 0; j < dim; j++ {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(strconv.FormatFloat((r.Float64()-0.5)*0.2, 'g', -1, 32))
		}
		sb.WriteByte(']')
	}
	sb.WriteString(`]]}`)
	data := []byte(sb.String())
	b.Run("encoding_json", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for i := 0; i < b.N; i++ {
			var out chromaQueryResponse
			if err := json.Unmarshal(data, &out); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("fast", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for i := 0; i < b.N; i++ {
			var out chromaQueryResponse
			if err := decodeChromaQueryResponse(data, &out); err != nil {
				b.Fatal(err)
			}
		}
	})
}
