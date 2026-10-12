package httpapi

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// Reference copies of the layout preview/apply before rendering was deferred.
func refLayoutPreview(l *prepareTurnMemoryReadingLayout, row prepareTurnMemoryReadingRow) prepareTurnMemoryReadingEdit {
	previous := l.groups[row.Group]
	if row.Group == "" {
		previous = nil
		row.rendered = strings.TrimSpace(row.Plain)
	} else {
		incoming, ref := row.Parts, row.Ref
		row.Parts = nil
		row.partRefs = map[prepareTurnMemoryPartIdentity]string{}
		seen := map[prepareTurnMemoryPartIdentity]bool{}
		if previous != nil {
			row.Order = minInt(row.Order, previous.Order)
			row.Header = previous.Header
			row.Parts = append(row.Parts, previous.Parts...)
			for _, part := range previous.Parts {
				seen[prepareTurnMemoryPartIdentity{part.Key, part.Text}] = true
			}
			for key, value := range previous.partRefs {
				row.partRefs[key] = value
			}
		}
		for _, part := range incoming {
			key := prepareTurnMemoryPartIdentity{part.Key, part.Text}
			shared := prepareTurnMemorySharedPartIdentity(part)
			if shared.Key != "" && l.currentParts[shared] {
				continue // Same source constituent already present in this input.
			}
			if !seen[key] {
				row.Parts = append(row.Parts, part)
				seen[key] = true
				if ref != "" {
					row.partRefs[key], ref = ref, ""
				}
			}
		}
		lines := []string{}
		for _, part := range row.Parts {
			text := part.Text
			if ref := row.partRefs[prepareTurnMemoryPartIdentity{part.Key, part.Text}]; ref != "" {
				text = "[" + ref + "] " + text
			}
			lines = append(lines, "  "+text)
		}
		row.rendered = ""
		if len(lines) > 0 {
			row.rendered = strings.TrimSpace("- " + row.Header + "\n" + strings.Join(lines, "\n"))
		}
	}
	cost := func(text string) int {
		if text == "" {
			return 0
		}
		return 1 + utf8.RuneCountInString(text)
	}
	delta := cost(row.rendered)
	if previous != nil {
		delta -= cost(previous.rendered)
	}
	return prepareTurnMemoryReadingEdit{delta: delta, row: row, previous: previous}
}

func refLayoutApply(l *prepareTurnMemoryReadingLayout, edit prepareTurnMemoryReadingEdit) string {
	row := &edit.row
	if edit.previous != nil {
		oldOrder := edit.previous.Order
		*edit.previous = edit.row
		row = edit.previous
		if row.Order != oldOrder {
			// Only an earlier borrowed selection changes group placement.
			sort.SliceStable(l.rows, func(i, j int) bool { return l.rows[i].Order < l.rows[j].Order })
		}
	} else {
		pos := sort.Search(len(l.rows), func(i int) bool { return l.rows[i].Order > row.Order })
		l.rows = append(l.rows, nil)
		copy(l.rows[pos+1:], l.rows[pos:])
		l.rows[pos] = row
	}
	if row.Group != "" {
		if l.groups == nil {
			l.groups = map[string]*prepareTurnMemoryReadingRow{}
		}
		l.groups[row.Group] = row
	}
	for _, part := range row.Parts {
		shared := prepareTurnMemorySharedPartIdentity(part)
		if shared.Key != "" && l.currentParts != nil {
			l.currentParts[shared] = true
		}
	}
	return row.rendered
}

func TestReadingLayoutDeferredRenderMatchesReference(t *testing.T) {
	r := rand.New(rand.NewSource(31))
	texts := []string{"", " ", "  ", "	", "a", "가나다", "state: 맑음", "x 　", " ", "tail  ", "ÿ", "aÃ", "Ω", "[S1] y"}
	refs := []string{"", "", "", "F1", "F22", "S3"}
	for c := 0; c < 3000; c++ {
		ours := &prepareTurnMemoryReadingLayout{currentParts: map[prepareTurnMemoryPartIdentity]bool{}, currentLookup: newPrepareTurnCurrentPartLookup()}
		ref := &prepareTurnMemoryReadingLayout{currentParts: map[prepareTurnMemoryPartIdentity]bool{}}
		for step := 0; step < 12; step++ {
			row := prepareTurnMemoryReadingRow{Order: r.Intn(20), FactID: fmt.Sprint("f", step), Plain: "- " + texts[r.Intn(len(texts))]}
			if r.Intn(5) != 0 {
				row.Group = fmt.Sprint("g", r.Intn(4))
				row.Header = []string{"", "Heading", " 머리 ", "þ"}[r.Intn(4)]
				row.Ref = refs[r.Intn(len(refs))]
				parts := r.Intn(5)
				if r.Intn(4) == 0 {
					parts = r.Intn(30) // past the row size scanned without a map
				}
				for p := 0; p < parts; p++ {
					part := prepareTurnMemoryFormPart{Key: fmt.Sprint("k", r.Intn(6)), Text: texts[r.Intn(len(texts))]}
					if r.Intn(3) == 0 {
						part.SharedKey = fmt.Sprint("@source/s/", part.Key)
					}
					row.Parts = append(row.Parts, part)
				}
			}
			// Mark incoming parts known to be distinct, as rendering does.
			distinct := true
			for i := range row.Parts {
				for j := 0; j < i; j++ {
					if row.Parts[i].Key == row.Parts[j].Key && row.Parts[i].Text == row.Parts[j].Text {
						distinct = false
					}
				}
			}
			if distinct != prepareTurnMemoryPartsDistinct(row.Parts) {
				t.Fatalf("case %d step %d: distinct %v, want %v", c, step, !distinct, distinct)
			}
			ourRow := row
			if distinct && r.Intn(2) == 0 {
				ourRow.distinctParts = ourRow.Parts
			}
			got, want := ours.preview(ourRow), refLayoutPreview(ref, row)
			if got.delta != want.delta {
				t.Fatalf("case %d step %d: delta %d, want %d (row %#v)", c, step, got.delta, want.delta, row)
			}
			if (got.row.Parts == nil) != (want.row.Parts == nil) || !reflect.DeepEqual(got.row.Parts, want.row.Parts) || !sameStringMap(got.row.partRefs, want.row.partRefs) {
				t.Fatalf("case %d step %d: parts %#v, want %#v", c, step, got.row.Parts, want.row.Parts)
			}
			if r.Intn(2) == 0 {
				if a, b := ours.apply(got), refLayoutApply(ref, want); a != b {
					t.Fatalf("case %d step %d: applied %q, want %q", c, step, a, b)
				}
			}
		}
		if a, b := strings.Join(ours.texts(), "|"), strings.Join(ref.texts(), "|"); a != b {
			t.Fatalf("case %d: layout %q, want %q", c, a, b)
		}
	}
}

// sameStringMap compares map contents; a nil and an empty map are the same.
func sameStringMap(a, b map[prepareTurnMemoryPartIdentity]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if other, ok := b[key]; !ok || other != value {
			return false
		}
	}
	return true
}
