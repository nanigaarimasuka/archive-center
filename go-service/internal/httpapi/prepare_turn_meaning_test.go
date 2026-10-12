package httpapi

import (
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMeaningTokenStandsForTheJoinedText(t *testing.T) {
	parts := []string{"Mira locked the archive door.", "current state: tired", "scene: night"}
	m := newPrepareTurnMeaning(parts)
	again := newPrepareTurnMeaning(append([]string(nil), parts...))
	if again != m || again.token != m.token {
		t.Fatal("an equal meaning was not shared")
	}
	other := newPrepareTurnMeaning([]string{"Mira locked the archive door.", "current state: tired\nscene: night"})
	if other.token != m.token {
		// The same joined text from other parts is the same meaning.
		t.Fatal("equal joined text got another token")
	}
	if different := newPrepareTurnMeaning([]string{"Mira opened the door."}); different.token == m.token {
		t.Fatal("different meanings share a token")
	}
	joined := strings.Join(parts, "\n")
	if prepareTurnMeaningText(m.token) != joined || prepareTurnMeaningText("plain text") != "plain text" {
		t.Fatal("token did not resolve to its joined text")
	}
	if !reflect.DeepEqual(prepareTurnPriorityAnalyzeText(m.token), prepareTurnPriorityAnalyzeText(joined)) {
		t.Fatal("analysis of a token differs from the analysis of its text")
	}
	runtime.KeepAlive(m)
}

func TestMeaningIsForgottenWithItsForms(t *testing.T) {
	token := newPrepareTurnMeaning([]string{"a meaning no form keeps", time.Now().String()}).token
	for i := 0; i < 20; i++ {
		runtime.GC()
		if _, ok := prepareTurnMeaningOf(token); !ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("an unreferenced meaning stayed registered")
}
