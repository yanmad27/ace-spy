package chatparse

import (
	"sort"
	"strings"
	"sync"
)

// Digits OCR commonly confuses in the game's bitmap font.
var digitConfusions = map[byte]string{
	'0': "896", '1': "7", '2': "7", '3': "985", '5': "986", '6': "085", '7': "12", '8': "0356", '9': "538",
}

func minutes(m *message) int {
	return int(m.hh[0]-'0')*600 + int(m.hh[1]-'0')*60 + int(m.mm[0]-'0')*10 + int(m.mm[1]-'0')
}

// fixTimestamps uses the fact that chat lines are in chronological order (top to
// bottom): a timestamp outside the range of its neighbours is re-read with
// commonly confused digits until it fits.
func fixTimestamps(msgs []*message) {
	for i, m := range msgs {
		lo, hi := -1, 24*60
		if i > 0 {
			lo = minutes(msgs[i-1])
		}
		if i+1 < len(msgs) {
			hi = minutes(msgs[i+1])
		}
		if lo > hi {
			continue // midnight wrap or neighbours themselves misread
		}
		if t := minutes(m); t >= lo && t <= hi {
			continue
		}
		clock := []byte(m.hh + m.mm)
		best, bestDist := "", 1<<30
		for pos := range clock {
			for _, alt := range []byte(digitConfusions[clock[pos]]) {
				c := append([]byte(nil), clock...)
				c[pos] = alt
				cand := &message{hh: string(c[:2]), mm: string(c[2:])}
				if !isClock(cand.hh, cand.mm) {
					continue
				}
				t := minutes(cand)
				if t < lo || t > hi {
					continue
				}
				if d := min(t-max(lo, 0), hi-t); best == "" || d < bestDist {
					best, bestDist = string(c), d
				}
			}
		}
		if best != "" {
			m.hh, m.mm = best[:2], best[2:]
		}
	}
}

// NameBook learns the player and tag names seen in a region and snaps OCR variants
// ("Shunpee", "5hnpee") to the most frequently read form ("Shopee").
type NameBook struct {
	mu     sync.Mutex
	counts map[string]int
}

const nameBookCap = 5000

func NewNameBook() *NameBook { return &NameBook{counts: map[string]int{}} }

func (b *NameBook) observe(name string) {
	b.counts[name]++
	if len(b.counts) <= nameBookCap {
		return
	}
	type kv struct {
		k string
		v int
	}
	all := make([]kv, 0, len(b.counts))
	for k, v := range b.counts {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].v < all[j].v })
	for _, e := range all[:len(all)/4] {
		delete(b.counts, e.k)
	}
}

func (b *NameBook) canonical(name string) string {
	best, bestCount := name, b.counts[name]
	for n, c := range b.counts {
		if c > bestCount && similarName(n, name) {
			best, bestCount = n, c
		}
	}
	return best
}

func similarName(a, b string) bool {
	ra, rb := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	if len(ra) < 4 || len(rb) < 4 {
		return false
	}
	longest := max(len(ra), len(rb))
	if longest-min(len(ra), len(rb)) > 2 {
		return false
	}
	return 1-float64(levenshtein(ra, rb))/float64(longest) >= 0.7
}

// Messages is like the package-level Messages, additionally correcting timestamps by
// chronological order and names against the names learned so far.
func (b *NameBook) Messages(lines []string) []string {
	var out []string
	for _, m := range b.MessagesColored(lines, nil) {
		out = append(out, m.Text)
	}
	return out
}

// MessagesColored is Messages with token colours (see Msg).
func (b *NameBook) MessagesColored(lines []string, colors [][]string) []Msg {
	msgs := assembleColored(lines, colors)
	var chat []*message
	for _, m := range msgs {
		if !m.notice {
			chat = append(chat, m)
		}
	}
	fixTimestamps(chat)
	b.mu.Lock()
	for _, m := range msgs {
		for _, n := range m.names {
			b.observe(n)
		}
	}
	for _, m := range msgs {
		for i, n := range m.names {
			m.names[i] = b.canonical(n)
		}
	}
	b.mu.Unlock()
	out := make([]Msg, len(msgs))
	for i, m := range msgs {
		out[i] = m.colored()
	}
	return out
}

func levenshtein(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
