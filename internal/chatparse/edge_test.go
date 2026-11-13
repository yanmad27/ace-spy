package chatparse

import "testing"

// A capture area that starts at the chat box's left edge yields a sliver of the
// first glyph ("’") before the channel symbol.
func TestLeftEdgeSliver(t *testing.T) {
	cases := map[string]string{
		"’ *[08:50] [Tổng thống] [FinalChapter] tru ae":  "*[08:50] [Tổng thống] [FinalChapter] tru ae",
		"’ ^[08:49] [BoMayConCave] [SP] 3 slot camp tru": "^[08:49] [BoMayConCave] [SP] 3 slot camp tru",
		"’ [08:53] [Audi] het r":                         "[08:53] [Audi] het r",
	}
	for in, want := range cases {
		if got := Messages([]string{in}); len(got) != 1 || got[0] != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
