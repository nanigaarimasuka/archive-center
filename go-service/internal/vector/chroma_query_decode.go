package vector

import (
	"bytes"
	"encoding/json"
	"runtime"
	"strconv"
	"sync"
	"unsafe"
)

// chromaQueryResponse is the /query result shape.
type chromaQueryResponse struct {
	IDs        [][]string         `json:"ids"`
	Documents  [][]string         `json:"documents"`
	Metadatas  [][]map[string]any `json:"metadatas"`
	Distances  [][]float64        `json:"distances"`
	Embeddings [][][]float32      `json:"embeddings"`
}

// decodeChromaQueryResponse has the result and error of json.Unmarshal(data,
// out) into a zero response. Stored embeddings are most of a query response
// and encoding/json is slow on float arrays, so a well-formed embeddings value
// is parsed directly with the same strconv conversion; the other fields still
// go through encoding/json. Anything unexpected falls back to json.Unmarshal.
func decodeChromaQueryResponse(data []byte, out *chromaQueryResponse) error {
	if decoded, ok := fastDecodeChromaQueryResponse(data); ok {
		*out = decoded
		return nil
	}
	*out = chromaQueryResponse{}
	return json.Unmarshal(data, out)
}

func fastDecodeChromaQueryResponse(data []byte) (chromaQueryResponse, bool) {
	var out chromaQueryResponse
	embeddings, ok := fastDecodeChromaEmbeddings(data, false, &out)
	if !ok {
		return chromaQueryResponse{}, false
	}
	out.Embeddings = embeddings
	return out, true
}

// chromaGetEmbeddingsResponse is the /get result shape when only embeddings
// are included.
type chromaGetEmbeddingsResponse struct {
	IDs        []string    `json:"ids"`
	Embeddings [][]float32 `json:"embeddings"`
}

// decodeChromaGetEmbeddingsResponse is decodeChromaQueryResponse for /get,
// whose embeddings value has one level less.
func decodeChromaGetEmbeddingsResponse(data []byte, out *chromaGetEmbeddingsResponse) error {
	var decoded chromaGetEmbeddingsResponse
	if embeddings, ok := fastDecodeChromaEmbeddings(data, true, &decoded); ok {
		decoded.Embeddings = nil
		if embeddings != nil {
			decoded.Embeddings = embeddings[0]
		}
		*out = decoded
		return nil
	}
	*out = chromaGetEmbeddingsResponse{}
	return json.Unmarshal(data, out)
}

// fastDecodeChromaEmbeddings parses the embeddings value directly and decodes
// the rest of the document into rest with encoding/json. With rowsOnly the
// value is [][]number and comes back as the single query of the result.
func fastDecodeChromaEmbeddings(data []byte, rowsOnly bool, rest any) ([][][]float32, bool) {
	start, end, layout, ok := chromaEmbeddingsValueSpan(data, rowsOnly)
	if !ok {
		return nil, false
	}
	embeddings, ok := parseChromaEmbeddingRows(data, layout)
	if !ok {
		return nil, false
	}
	// The rest of the document, with the embeddings value replaced by null,
	// is decoded (and validated) by encoding/json as before.
	others := make([]byte, 0, len(data)-(end-start)+len("null"))
	others = append(others, data[:start]...)
	others = append(others, "null"...)
	others = append(others, data[end:]...)
	if err := json.Unmarshal(others, rest); err != nil {
		return nil, false
	}
	return embeddings, true
}

// chromaEmbeddingsValueSpan finds the value of the single top-level key that
// encoding/json would bind to Embeddings. It fails on escaped keys, repeated
// matching keys, or a malformed top-level object.
func chromaEmbeddingsValueSpan(data []byte, rowsOnly bool) (int, int, *chromaEmbeddingLayout, bool) {
	i := skipJSONSpace(data, 0)
	if i >= len(data) || data[i] != '{' {
		return 0, 0, nil, false
	}
	i = skipJSONSpace(data, i+1)
	if i < len(data) && data[i] == '}' {
		return 0, 0, nil, false
	}
	start, end := -1, -1
	var layout *chromaEmbeddingLayout
	for {
		if i >= len(data) || data[i] != '"' {
			return 0, 0, nil, false
		}
		keyEnd, escaped, ok := skipJSONString(data, i)
		if !ok {
			return 0, 0, nil, false
		}
		key := data[i+1 : keyEnd-1]
		i = skipJSONSpace(data, keyEnd)
		if i >= len(data) || data[i] != ':' {
			return 0, 0, nil, false
		}
		valueStart := skipJSONSpace(data, i+1)
		if escaped {
			return 0, 0, nil, false
		}
		var valueEnd int
		if bytes.EqualFold(key, []byte("embeddings")) {
			if start >= 0 {
				return 0, 0, nil, false
			}
			// Locating the rows also finds the end of this large value.
			valueEnd, layout, ok = scanChromaEmbeddings(data, valueStart, rowsOnly)
			if !ok {
				return 0, 0, nil, false
			}
			start, end = valueStart, valueEnd
		} else if valueEnd, ok = skipJSONValue(data, valueStart); !ok {
			return 0, 0, nil, false
		}
		i = skipJSONSpace(data, valueEnd)
		if i >= len(data) {
			return 0, 0, nil, false
		}
		if data[i] == ',' {
			i = skipJSONSpace(data, i+1)
			continue
		}
		if data[i] != '}' {
			return 0, 0, nil, false
		}
		if skipJSONSpace(data, i+1) != len(data) || start < 0 {
			return 0, 0, nil, false
		}
		return start, end, layout, true
	}
}

func skipJSONSpace(data []byte, i int) int {
	for i < len(data) {
		switch data[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// skipJSONString returns the index after the closing quote of the string at i.
func skipJSONString(data []byte, i int) (int, bool, bool) {
	escaped := false
	for j := i + 1; j < len(data); j++ {
		switch data[j] {
		case '\\':
			escaped = true
			j++
		case '"':
			return j + 1, escaped, true
		}
	}
	return 0, false, false
}

// skipJSONValue returns the end of the value at i. Values other than the
// embeddings are validated later by encoding/json.
func skipJSONValue(data []byte, i int) (int, bool) {
	if i >= len(data) {
		return 0, false
	}
	switch data[i] {
	case '"':
		end, _, ok := skipJSONString(data, i)
		return end, ok
	case '{', '[':
		depth := 0
		for j := i; j < len(data); j++ {
			switch data[j] {
			case '"':
				end, _, ok := skipJSONString(data, j)
				if !ok {
					return 0, false
				}
				j = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1, true
				}
			}
		}
		return 0, false
	default:
		j := i
		for j < len(data) {
			switch data[j] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				return j, j > i
			}
			j++
		}
		return j, j > i
	}
}

// chromaEmbeddingLayout is the shape of an embeddings value: for each query,
// the byte spans of its rows. A nil layout is a null value.
type chromaEmbeddingLayout struct {
	queries [][][2]int
}

// scanChromaEmbeddings locates the rows of the [][][]number (or null) value at
// i and returns the end of the value; with rowsOnly the value is [][]number.
// Rows hold only numbers, so a row ends at its first ']';
// parseChromaEmbeddingRows checks each row strictly.
func scanChromaEmbeddings(data []byte, i int, rowsOnly bool) (int, *chromaEmbeddingLayout, bool) {
	if bytes.HasPrefix(data[i:], []byte("null")) {
		return i + len("null"), nil, true
	}
	p := chromaFloatParser{b: data, i: i}
	layout := &chromaEmbeddingLayout{}
	scanRows := func() (struct{}, bool) {
		rows := [][2]int{}
		_, ok := parseJSONArray(&p, func() (struct{}, bool) {
			start := p.i
			if start >= len(data) || data[start] != '[' {
				return struct{}{}, false
			}
			close := bytes.IndexByte(data[start+1:], ']')
			if close < 0 {
				return struct{}{}, false
			}
			p.i = start + 1 + close + 1
			rows = append(rows, [2]int{start, p.i})
			return struct{}{}, true
		})
		layout.queries = append(layout.queries, rows)
		return struct{}{}, ok
	}
	var ok bool
	if rowsOnly {
		_, ok = scanRows()
	} else {
		_, ok = parseJSONArray(&p, scanRows)
	}
	if !ok {
		return 0, nil, false
	}
	return p.i, layout, true
}

// parseChromaEmbeddingRows converts the located rows concurrently; rows are
// independent. Empty arrays stay empty and non-nil, as with encoding/json.
func parseChromaEmbeddingRows(data []byte, layout *chromaEmbeddingLayout) ([][][]float32, bool) {
	if layout == nil {
		return nil, true
	}
	type rowRef struct{ query, row int }
	out := make([][][]float32, len(layout.queries))
	var refs []rowRef
	for q, rows := range layout.queries {
		out[q] = make([][]float32, len(rows))
		for r := range rows {
			refs = append(refs, rowRef{q, r})
		}
	}
	failed := make([]bool, len(refs))
	parallelRows(len(refs), func(i int) {
		ref := refs[i]
		span := layout.queries[ref.query][ref.row]
		rp := chromaFloatParser{b: data[:span[1]], i: span[0]}
		row, ok := parseJSONArrayInto(&rp, []float32{}, rp.float32)
		if !ok || rp.i != span[1] {
			failed[i] = true
			return
		}
		out[ref.query][ref.row] = row
	})
	for _, f := range failed {
		if f {
			return nil, false
		}
	}
	return out, true
}

type chromaFloatParser struct {
	b []byte
	i int
}

func (p *chromaFloatParser) space() { p.i = skipJSONSpace(p.b, p.i) }

func parseJSONArray[T any](p *chromaFloatParser, elem func() (T, bool)) ([]T, bool) {
	return parseJSONArrayInto(p, []T{}, elem)
}

// parseJSONArrayInto appends the elements of the array at p to out. An empty
// array leaves out empty and non-nil, as encoding/json does.
func parseJSONArrayInto[T any](p *chromaFloatParser, out []T, elem func() (T, bool)) ([]T, bool) {
	p.space()
	if p.i >= len(p.b) || p.b[p.i] != '[' {
		return nil, false
	}
	p.i++
	p.space()
	if p.i < len(p.b) && p.b[p.i] == ']' {
		p.i++
		return out, true
	}
	for {
		p.space()
		value, ok := elem()
		if !ok {
			return nil, false
		}
		out = append(out, value)
		p.space()
		if p.i >= len(p.b) {
			return nil, false
		}
		switch p.b[p.i] {
		case ',':
			p.i++
		case ']':
			p.i++
			return out, true
		default:
			return nil, false
		}
	}
}

// float32 reads a JSON number and converts it as encoding/json does for a
// float32 field: strconv.ParseFloat(literal, 32). Out-of-range values fail.
func (p *chromaFloatParser) float32() (float32, bool) {
	start := p.i
	b := p.b
	i := start
	if i < len(b) && b[i] == '-' {
		i++
	}
	switch {
	case i < len(b) && b[i] == '0':
		i++
	case i < len(b) && b[i] >= '1' && b[i] <= '9':
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	default:
		return 0, false
	}
	if i < len(b) && b[i] == '.' {
		i++
		digits := i
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
		if i == digits {
			return 0, false
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		digits := i
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
		if i == digits {
			return 0, false
		}
	}
	literal := b[start:i]
	value, err := strconv.ParseFloat(unsafe.String(unsafe.SliceData(literal), len(literal)), 32)
	if err != nil {
		return 0, false
	}
	p.i = i
	return float32(value), true
}

// parallelRows runs fn for each index on up to GOMAXPROCS goroutines.
func parallelRows(n int, fn func(int)) {
	workers := min(runtime.GOMAXPROCS(0), n)
	if workers <= 1 {
		for i := 0; i < n; i++ {
			fn(i)
		}
		return
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := w; i < n; i += workers {
				fn(i)
			}
		}(w)
	}
	wg.Wait()
}
