// Package chatdiff turns successive OCR snapshots of a scrolling chat box into
// a stream of newly appeared lines, tolerating small OCR differences between frames.
package chatdiff

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const (
	historyCap    = 500
	minLineRunes  = 2
	similarityMin = 0.8
)

type Tracker struct {
	history []string
}

func Normalize(s string) string {
	s = norm.NFC.String(s)
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
}

// Feed takes the lines currently visible (top to bottom, newest last) and returns
// the lines that were not present in previously seen frames.
func (t *Tracker) Feed(lines []string) []string {
	cur := make([]string, 0, len(lines))
	for _, l := range lines {
		if n := Normalize(l); len([]rune(n)) >= minLineRunes {
			cur = append(cur, n)
		}
	}
	if len(cur) == 0 {
		return nil
	}

	k := overlap(t.history, cur)
	fresh := cur[k:]
	if k == 0 && len(t.history) > 0 {
		// No alignment: either everything scrolled past since the last frame, or the
		// previous frame was misread. Suppress lines seen in the recent window.
		window := t.history[max(0, len(t.history)-len(cur)-5):]
		filtered := fresh[:0:0]
		for _, l := range fresh {
			if !containsSimilar(window, l) {
				filtered = append(filtered, l)
			}
		}
		fresh = filtered
	}

	t.history = append(t.history, fresh...)
	if len(t.history) > historyCap {
		t.history = append([]string(nil), t.history[len(t.history)-historyCap:]...)
	}
	return fresh
}

// overlap returns the largest k such that the last k history lines match the first k current lines.
func overlap(history, cur []string) int {
	for k := min(len(history), len(cur)); k > 0; k-- {
		off := len(history) - k
		ok := true
		for i := 0; i < k; i++ {
			if !Similar(history[off+i], cur[i]) {
				ok = false
				break
			}
		}
		if ok {
			return k
		}
	}
	return 0
}

func containsSimilar(lines []string, s string) bool {
	for _, l := range lines {
		if Similar(l, s) {
			return true
		}
	}
	return false
}

func Similar(a, b string) bool {
	if a == b {
		return true
	}
	ra, rb := []rune(a), []rune(b)
	longest := max(len(ra), len(rb))
	if longest == 0 {
		return true
	}
	if float64(min(len(ra), len(rb)))/float64(longest) < similarityMin {
		return false
	}
	return 1-float64(levenshtein(ra, rb))/float64(longest) >= similarityMin
}

func levenshtein(a, b []rune) int {
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}
