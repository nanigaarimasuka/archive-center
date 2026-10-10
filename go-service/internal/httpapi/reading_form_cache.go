package httpapi

import (
	"container/list"
	"math"
	"os"
	"strconv"
	"strings"
	"unsafe"

	"github.com/maypok86/otter/v2"
)

// Turns read mostly the same stored memories, so they build mostly the same
// reading forms again. Outside debug mode, forms are kept across requests.
// A form key is its group (source and path), the digest of its reading's
// whole content and its ordered reference bindings, and a form is built from
// exactly these, so a kept form equals the one a request would build. Forms
// are never changed after they are built. Debug mode always builds its own,
// so its output and measurements stay those of perf/exact.

// The default bound on the estimated bytes of kept forms;
// AC_READING_FORM_CACHE_MB overrides it, and 0 turns keeping off.
const readingFormCacheDefaultMB = 512

// readingFormCache is nil when keeping is off.
type readingFormCache struct {
	forms *otter.Cache[prepareTurnMemoryFormKey, *prepareTurnMemoryForm]
}

func newReadingFormCache(budget int) *readingFormCache {
	if budget <= 0 {
		return nil
	}
	return &readingFormCache{otter.Must(&otter.Options[prepareTurnMemoryFormKey, *prepareTurnMemoryForm]{
		MaximumWeight: uint64(budget),
		Weigher: func(key prepareTurnMemoryFormKey, form *prepareTurnMemoryForm) uint32 {
			return uint32(min(readingFormSize(key, form), math.MaxUint32))
		},
	})}
}

func readingFormCacheBudget() int {
	mb := readingFormCacheDefaultMB
	if value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("AC_READING_FORM_CACHE_MB"))); err == nil && value >= 0 {
		mb = value
	}
	return mb << 20
}

var sharedReadingFormCache = newReadingFormCache(readingFormCacheBudget())

func keptReadingFormCache() *readingFormCache {
	return sharedReadingFormCache
}

// get returns the kept form for key, if any.
func (c *readingFormCache) get(key prepareTurnMemoryFormKey) *prepareTurnMemoryForm {
	if c == nil {
		return nil
	}
	form, _ := c.forms.GetIfPresent(key)
	return form
}

// put keeps a build's new forms as far as the budget allows.
func (c *readingFormCache) put(keys []prepareTurnMemoryFormKey, forms []*prepareTurnMemoryForm) {
	if c == nil {
		return
	}
	for i, key := range keys {
		form := forms[i]
		if form.kept == nil {
			form.kept = &prepareTurnKeptForm{source: form.Parts}
		}
		c.forms.SetIfAbsent(key, form)
	}
}

// readingFormSize overestimates what keeping a form holds: strings shared
// with other forms are counted for each of them.
func readingFormSize(key prepareTurnMemoryFormKey, form *prepareTurnMemoryForm) int {
	const stringHeader = int(unsafe.Sizeof(""))
	size := int(unsafe.Sizeof(prepareTurnMemoryForm{})+unsafe.Sizeof(list.Element{})) + 64 // entry and table slot
	size += len(key.Group) + len(key.References)
	size += len(form.Group) + len(form.Heading) + len(form.Text) + len(form.Meaning)
	// The meaning's kept analysis (its terms and own needle segments) is
	// added later and is about as large as the meaning.
	size += len(form.Meaning)
	for _, ref := range form.Refs {
		size += stringHeader + len(ref)
	}
	for _, part := range form.Parts {
		size += int(unsafe.Sizeof(part)) + len(part.Key) + len(part.Text) + len(part.SharedKey)
		for _, ref := range part.Refs {
			size += stringHeader + len(ref)
		}
		if part.DeliveryText != nil {
			size += stringHeader + len(*part.DeliveryText)
		}
	}
	return size
}
