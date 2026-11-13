package glyphocr

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestChannelWidths(t *testing.T) {
	dir := os.Getenv("COLOR_FRAMES")
	if dir == "" {
		t.Skip()
	}
	e, _ := New()
	files, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	type wk struct{ ch string }
	symW := map[string]map[int]int{}
	timeW := map[string]map[int]int{}
	odd := map[string]int{}
	for _, f := range files {
		pix, w, h := loadNative(t, f, 2)
		lines, _ := e.RecognizeLines(pix, w, h)
		for _, l := range lines {
			sp := l.Spans
			i := -1
			for k := 0; k+1 < len(sp); k++ {
				if sp[k].Text == "[" && sp[k+1].Text >= "0" && sp[k+1].Text <= "9" {
					i = k
					break
				}
			}
			if i < 0 {
				continue
			}
			ch := ""
			start := sp[i].X
			if i > 0 && strings.Contains("!@#$%^*~", sp[i-1].Text) {
				ch = sp[i-1].Text
				start = sp[i-1].X
			} else if i > 0 && sp[i-1].Text != " " {
				odd["before [ = "+sp[i-1].Text]++
			}
			j := i
			for j < len(sp) && sp[j].Text != "]" {
				j++
			}
			if j >= len(sp) || j-i != 6 {
				continue
			}
			if symW[ch] == nil {
				symW[ch], timeW[ch] = map[int]int{}, map[int]int{}
			}
			symW[ch][sp[i].X-start]++
			timeW[ch][sp[j].X+sp[j].W-sp[i].X]++
		}
	}
	keys := make([]string, 0)
	for k := range symW {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("W\t%q\tsymbol width px %v\t[hh:mm] width px %v", k, symW[k], timeW[k])
	}
	for k, v := range odd {
		t.Logf("ODD\t%q\t%d", k, v)
	}
}
