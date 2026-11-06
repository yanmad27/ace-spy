package chatdiff

import (
	"reflect"
	"testing"
)

func TestFeed(t *testing.T) {
	var tr Tracker
	steps := []struct {
		frame []string
		want  []string
	}{
		{[]string{"A: xin chào", "B: chào bạn"}, []string{"A: xin chào", "B: chào bạn"}},
		{[]string{"A: xin chào", "B: chào bạn"}, nil},
		{[]string{"B: chào bạn", "C: đi săn không"}, []string{"C: đi săn không"}},
		{[]string{"B: chao bạn", "C: đi săn khöng", "D: ok"}, []string{"D: ok"}},
		{[]string{"E: mới 1", "F: mới 2"}, []string{"E: mới 1", "F: mới 2"}},
		{[]string{"F: mới 2", "F: mới 2"}, []string{"F: mới 2"}},
		{[]string{"", " ", "x"}, nil},
	}
	for i, s := range steps {
		got := tr.Feed(s.frame)
		if len(got) == 0 && len(s.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, s.want) {
			t.Fatalf("step %d: got %q want %q", i, got, s.want)
		}
	}
}

func TestFeedSuppressesRecentOnMisalignment(t *testing.T) {
	var tr Tracker
	tr.Feed([]string{"A: một", "B: hai", "C: ba"})
	got := tr.Feed([]string{"zzzz garbage", "C: ba", "D: bốn"})
	want := []string{"zzzz garbage", "D: bốn"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestNormalizeComposesDiacritics(t *testing.T) {
	decomposed := "Việt   Nam"
	if got := Normalize(decomposed); got != "Việt Nam" {
		t.Fatalf("got %q", got)
	}
}
