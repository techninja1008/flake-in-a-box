package rewrite

import (
	"bytes"
	"strings"
	"testing"
)

func TestChunkingInvariant(t *testing.T) {
	in := strings.Repeat("xxAAAAyyAAAAzz", 50)
	var want bytes.Buffer
	w := New(map[string]string{"AAAA": "BBBB"}, &want, true)
	w.Write([]byte(in))
	w.Flush()
	wantM := w.SortedMatches()
	for chunk := 1; chunk < 9; chunk++ {
		var got bytes.Buffer
		w := New(map[string]string{"AAAA": "BBBB"}, &got, true)
		for i := 0; i < len(in); i += chunk {
			end := i + chunk
			if end > len(in) {
				end = len(in)
			}
			w.Write([]byte(in[i:end]))
		}
		w.Flush()
		if got.String() != want.String() {
			t.Fatalf("chunk %d: output differs", chunk)
		}
		if len(w.SortedMatches()) != len(wantM) {
			t.Fatalf("chunk %d: matches differ", chunk)
		}
		for i, m := range w.SortedMatches() {
			if m != wantM[i] {
				t.Fatalf("chunk %d: match %d at %d, want %d", chunk, i, m, wantM[i])
			}
		}
	}
	if !strings.Contains(want.String(), "BBBB") || strings.Contains(want.String(), "AAAA") {
		t.Fatal("rewrite not applied")
	}
}
