package httpapi

import (
	"crypto/sha256"
	"fmt"
	"sync"
)

// One linked scope record. Its encoding is fixed once indexed, so request-wide
// reuse never re-marshals the same boundary for each alias, pass or seed.
type prepareTurnKnowledgeBoundaryRecord struct {
	value   map[string]any
	encoded string
	partKey string
}

func newPrepareTurnKnowledgeBoundaryRecord(boundary map[string]any) *prepareTurnKnowledgeBoundaryRecord {
	encoded := mustCompactJSON(boundary)
	sum := sha256.Sum256([]byte(encoded))
	return &prepareTurnKnowledgeBoundaryRecord{value: boundary, encoded: encoded, partKey: fmt.Sprintf("@knowledge/%x", sum[:8])}
}

// The linked-scope graph depends only on immutable request sources. It is read
// by every carry in one request and never by another request.
type prepareTurnKnowledgeIndex struct {
	byRef    map[string][]*prepareTurnKnowledgeBoundaryRecord
	sessions map[string]string
	payloads map[string]map[string]any
}

type prepareTurnKnowledgeIndexCache struct {
	once  sync.Once
	index *prepareTurnKnowledgeIndex
}

func prepareTurnCloneKnowledgeValue(value any) any {
	// nil and empty values encode differently and stay distinct.
	switch v := value.(type) {
	case map[string]any:
		if v == nil {
			return v
		}
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = prepareTurnCloneKnowledgeValue(item)
		}
		return out
	case []any:
		if v == nil {
			return v
		}
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = prepareTurnCloneKnowledgeValue(item)
		}
		return out
	case []string:
		if v == nil {
			return v
		}
		return append(make([]string, 0, len(v)), v...)
	case []map[string]any:
		if v == nil {
			return v
		}
		out := make([]map[string]any, len(v))
		for i, item := range v {
			out[i], _ = prepareTurnCloneKnowledgeValue(item).(map[string]any)
		}
		return out
	}
	return value
}
