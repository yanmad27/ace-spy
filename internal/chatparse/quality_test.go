package chatparse

import "testing"

func TestQualityPrefersCleanReading(t *testing.T) {
	for _, c := range [][2]string{
		{"^[10:25] [ChymD] [BC] tầm nàv chỉ có DGA", "^[10:25] [ChymD] [BC] tầm này chỉ có DGA"},
		{"^[10:25] [ChymD] [BC] cho em xin 1 câv với", "^[10:25] [ChymD] [BC] cho em xin 1 cây với"},
		{"^[10:25] [ChymD] [BC] call tru·'.", "^[10:25] [ChymD] [BC] call tru"},
	} {
		if Quality(c[1]) <= Quality(c[0]) {
			t.Errorf("Quality(%q)=%d <= Quality(%q)=%d", c[1], Quality(c[1]), c[0], Quality(c[0]))
		}
	}
	h, b, ok := Split("^[10:25] [ChymD] [BC] cho em")
	if !ok || h != "^[10:25] [ChymD] [BC]" || b != "cho em" {
		t.Fatalf("Split = %q %q %v", h, b, ok)
	}
}
