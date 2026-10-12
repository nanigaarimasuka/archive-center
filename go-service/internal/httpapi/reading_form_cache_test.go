package httpapi

import (
	"fmt"
	"testing"
)

func readingFormCacheTestForm(i int) (prepareTurnMemoryFormKey, *prepareTurnMemoryForm) {
	key := prepareTurnMemoryFormKey{Group: fmt.Sprintf("g%03d", i)}
	return key, &prepareTurnMemoryForm{Group: key.Group, Text: string(make([]byte, 1000))}
}

func TestReadingFormCacheKeepsFormsWithinBudget(t *testing.T) {
	key0, form0 := readingFormCacheTestForm(0)
	size := readingFormSize(key0, form0)
	cache := newReadingFormCache(size * 3)
	defer cache.forms.StopAllGoroutines()
	keys, forms := []prepareTurnMemoryFormKey{}, []*prepareTurnMemoryForm{}
	for i := 0; i < 2; i++ {
		key, form := readingFormCacheTestForm(i)
		keys, forms = append(keys, key), append(forms, form)
	}
	cache.put(keys, forms)
	for i := range keys {
		if got := cache.get(keys[i]); got != forms[i] {
			t.Fatalf("form %d: got %p, want the kept %p", i, got, forms[i])
		}
	}
	// A form already kept is not replaced.
	_, other := readingFormCacheTestForm(0)
	cache.put(keys[:1], []*prepareTurnMemoryForm{other})
	if cache.get(keys[0]) != forms[0] {
		t.Fatal("kept form replaced")
	}
	for i := 2; i < 20; i++ {
		key, form := readingFormCacheTestForm(i)
		cache.put([]prepareTurnMemoryFormKey{key}, []*prepareTurnMemoryForm{form})
	}
	cache.forms.CleanUp()
	if weight := cache.forms.WeightedSize(); weight > uint64(size*3) {
		t.Fatalf("kept weight %d over budget %d", weight, size*3)
	}
}

func TestReadingFormCacheOff(t *testing.T) {
	if newReadingFormCache(0) != nil {
		t.Fatal("zero budget made a cache")
	}
	var cache *readingFormCache
	key, form := readingFormCacheTestForm(1)
	cache.put([]prepareTurnMemoryFormKey{key}, []*prepareTurnMemoryForm{form})
	if cache.get(key) != nil {
		t.Fatal("nil cache returned a form")
	}
}
