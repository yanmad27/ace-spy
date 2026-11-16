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
	partial map[string]bool // history lines read with hidden glyph rows
	// Better reports whether reading b of a line is better than reading a. When set,
	// a line already seen that is re-read better (e.g. once it scrolls clear of
	// something covering it) is returned again with the better reading.
	Better func(a, b string) bool
}

func Normalize(s string) string {
	s = norm.NFC.String(s)
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
}

// Fresh is a line to publish; Revises is the earlier reading it replaces, if any.
type Fresh struct {
	Text    string
	Revises string
}

// Feed takes the lines currently visible (top to bottom, newest last) and returns
// the lines that were not present in previously seen frames, preceded by better
// re-readings of lines already returned (see Better).
func (t *Tracker) Feed(lines []string) []string {
	var out []string
	for _, f := range t.FeedFlagged(lines, nil) {
		out = append(out, f.Text)
	}
	return out
}

// FeedFlagged is Feed with per-line partial flags. A line first read partially
// (glyph rows hidden) is re-published when a complete reading of it differs.
func (t *Tracker) FeedFlagged(lines []string, partial []bool) []Fresh {
	cur := make([]string, 0, len(lines))
	curPartial := make([]bool, 0, len(lines))
	for i, l := range lines {
		if n := Normalize(l); len([]rune(n)) >= minLineRunes {
			cur = append(cur, n)
			curPartial = append(curPartial, i < len(partial) && partial[i])
		}
	}
	if len(cur) == 0 {
		return nil
	}
	if t.partial == nil {
		t.partial = map[string]bool{}
	}

	k := overlap(t.history, cur)
	var out []Fresh
	off := len(t.history) - k
	for i := 0; i < k; i++ {
		old := t.history[off+i]
		if old == cur[i] {
			if !curPartial[i] {
				delete(t.partial, old)
			}
			continue
		}
		completed := t.partial[old] && !curPartial[i]
		if completed || (t.Better != nil && t.Better(old, cur[i])) {
			t.history[off+i] = cur[i]
			delete(t.partial, old)
			if curPartial[i] {
				t.partial[cur[i]] = true
			}
			out = append(out, Fresh{Text: cur[i], Revises: old})
		}
	}
	fresh := cur[k:]
	freshPartial := curPartial[k:]
	if k == 0 && len(t.history) > 0 {
		// No alignment: either everything scrolled past since the last frame, or the
		// previous frame was misread. Suppress lines seen in the recent window.
		window := t.history[max(0, len(t.history)-len(cur)-5):]
		filtered := fresh[:0:0]
		var filteredPartial []bool
		for i, l := range fresh {
			if !containsSimilar(window, l) {
				filtered = append(filtered, l)
				filteredPartial = append(filteredPartial, freshPartial[i])
			}
		}
		fresh, freshPartial = filtered, filteredPartial
	}

	for i, l := range fresh {
		if freshPartial[i] {
			t.partial[l] = true
		}
		out = append(out, Fresh{Text: l})
	}
	t.history = append(t.history, fresh...)
	if len(t.history) > historyCap {
		for _, l := range t.history[:len(t.history)-historyCap] {
			delete(t.partial, l)
		}
		t.history = append([]string(nil), t.history[len(t.history)-historyCap:]...)
	}
	return out
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
