// Package chatparse turns raw OCR lines from the Ace Online chat box into whole,
// meaningful Vietnamese chat messages ("[hh:mm] [Name] text"), dropping OCR noise.
package chatparse

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// A chat line is "<channel>[hh:mm] [Name] text", e.g. "![11:05] [Diana] ...".
// OCR often misreads the channel symbol, brackets and digits ("I[11:05)", "[11:2D]",
// "$l1:05]"), so the pattern is loose.
var timestampRe = regexp.MustCompile(`^\s*(\S{0,2}?)\s*[\[\(]?\s*([0-9IlOoDS]{1,2})\s*[:;.]\s*([0-9IlOoDS]{2,3})\s*[\]\)|Il1]?`)

// Channel symbols as shown in game: ! green, * blue, @ purple, # $ % orange, ^ red.
const channelSymbols = "!*@#$%^"

var channelLookalikes = map[rune]rune{
	'¡': '!', 'ǃ': '!', 'I': '!', '|': '!', 'l': '!', 'i': '!', '1': '!',
	'©': '@', '®': '@',
	'‡': '#',
	'S': '$', 's': '$', '§': '$',
	'ˆ': '^',
}

func channelOf(prefix string) string {
	for _, r := range prefix {
		if strings.ContainsRune(channelSymbols, r) {
			return string(r)
		}
	}
	for _, r := range prefix {
		if c, ok := channelLookalikes[r]; ok {
			return string(c)
		}
	}
	return ""
}

// Header groups such as "[Tổng tư lệnh] [valHellsing]" or "IANTD-HID]" right after the timestamp.
var nameGroupRe = regexp.MustCompile(`^\s*[\[\(|Il]?([^\[\]\(\)]{1,32})[\]\)]`)

var digitFix = strings.NewReplacer("I", "1", "l", "1", "O", "0", "o", "0", "D", "0", "S", "5")

// Messages assembles visible OCR lines (top to bottom) into meaningful messages.
// Only text that starts with a "[hh:mm]" header (plus its wrapped lines) is kept.
func Messages(lines []string) []string {
	var out []string
	var cur *message
	flush := func() {
		if cur != nil && cur.meaningful() {
			out = append(out, cur.String())
		}
		cur = nil
	}
	for _, raw := range lines {
		line := clean(raw)
		if line == "" {
			continue
		}
		if m, ok := parseHeader(line); ok {
			flush()
			cur = m
			continue
		}
		if cur != nil {
			if isMeaningfulLine(line) {
				cur.body = strings.TrimSpace(cur.body + " " + line)
				continue
			}
			flush()
			continue
		}
		// Lines above the first timestamp are the tail of a message that scrolled off.
	}
	flush()
	return out
}

type message struct {
	channel string
	hh, mm  string
	names   []string
	body    string
}

func (m *message) String() string {
	var b strings.Builder
	b.WriteString(m.channel + "[" + m.hh + ":" + m.mm + "]")
	for _, n := range m.names {
		b.WriteString(" [" + n + "]")
	}
	if m.body != "" {
		b.WriteString(" " + m.body)
	}
	return b.String()
}

func (m *message) meaningful() bool {
	if strings.HasPrefix(m.body, "[") {
		return false // unclosed name bracket: the header was cut or misread
	}
	st := score(m.body)
	enough := st.valid >= 2 || (st.words == 1 && st.valid == 1 && st.longest >= 3)
	return enough && st.ratio() >= 0.5 && st.junkRatio() <= 0.2
}

func parseHeader(line string) (*message, bool) {
	loc := timestampRe.FindStringSubmatchIndex(line)
	if loc == nil {
		return nil, false
	}
	channel := channelOf(line[loc[2]:loc[3]])
	hh := digitFix.Replace(line[loc[4]:loc[5]])
	mm := digitFix.Replace(line[loc[6]:loc[7]])
	if len(mm) == 3 {
		mm = mm[:2]
	}
	if len(hh) == 1 {
		hh = "0" + hh
	}
	if !isClock(hh, mm) {
		return nil, false
	}
	m := &message{channel: channel, hh: hh, mm: mm}
	rest := line[loc[1]:]
	for len(m.names) < 3 {
		g := nameGroupRe.FindStringSubmatchIndex(rest)
		if g == nil {
			break
		}
		name := strings.Trim(rest[g[2]:g[3]], " .,:;'\"`|")
		if name != "" {
			m.names = append(m.names, name)
		}
		rest = rest[g[1]:]
	}
	m.body = strings.TrimLeft(strings.TrimSpace(rest), ".,:;|`'\"")
	m.body = strings.TrimSpace(m.body)
	return m, true
}

func isClock(hh, mm string) bool {
	if len(hh) != 2 || len(mm) != 2 {
		return false
	}
	for _, r := range hh + mm {
		if r < '0' || r > '9' {
			return false
		}
	}
	return hh <= "23" && mm <= "59"
}

func clean(s string) string {
	s = norm.NFC.String(s)
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
}

// isMeaningfulLine is the stricter test for lines without a timestamp header.
func isMeaningfulLine(line string) bool {
	st := score(line)
	return st.valid >= 2 && st.ratio() >= 0.6 && st.junkRatio() <= 0.12 && st.singleRatio() <= 0.25
}

// Acceptable reports whether a single pushed text is a well-formed, meaningful
// "<channel>[hh:mm] [Name] text" chat message.
func Acceptable(s string) bool {
	m, ok := parseHeader(clean(s))
	return ok && m.meaningful()
}

type stats struct {
	valid, words, singles, junk, runes, longest int
}

func (s stats) ratio() float64 {
	if s.words == 0 {
		return 0
	}
	return float64(s.valid) / float64(s.words)
}

func (s stats) junkRatio() float64 {
	if s.runes == 0 {
		return 1
	}
	return float64(s.junk) / float64(s.runes)
}

func (s stats) singleRatio() float64 {
	if s.words == 0 {
		return 1
	}
	return float64(s.singles) / float64(s.words)
}

const allowedPunct = ".,!?:;'\"()[]+-/%&@#*=~_<>"

func score(text string) stats {
	var st stats
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		st.runes++
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(allowedPunct, r) {
			st.junk++
		}
	}
	for _, tok := range strings.Fields(text) {
		tok = strings.TrimFunc(tok, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if tok == "" || isNumeric(tok) {
			continue
		}
		st.words++
		n := len([]rune(tok))
		st.longest = max(st.longest, n)
		if n == 1 {
			st.singles++
		}
		if IsSyllable(tok) || chatWords[strings.ToLower(tok)] {
			st.valid++
		}
	}
	return st
}

// isNumeric accepts amounts like "10", "+7", "93/1", "200k", "1.5tr".
func isNumeric(tok string) bool {
	digits := 0
	for _, r := range tok {
		if unicode.IsDigit(r) {
			digits++
		}
	}
	if digits == 0 {
		return false
	}
	t := strings.ToLower(strings.TrimRight(strings.ToLower(tok), "ktr%"))
	for _, r := range t {
		if !unicode.IsDigit(r) && !strings.ContainsRune(".,/+-:", r) {
			return false
		}
	}
	return true
}

// Common chat abbreviations / teencode that are not well-formed syllables.
var chatWords = map[string]bool{
	"k": true, "ko": true, "kg": true, "khg": true, "dc": true, "đc": true, "dk": true, "đk": true,
	"vs": true, "mn": true, "ok": true, "oke": true, "cmm": true, "vl": true, "vcl": true, "cc": true,
	"ae": true, "ad": true, "bn": true, "mk": true, "mik": true, "nt": true, "ib": true, "pm": true,
	"j": true, "z": true, "r": true, "ns": true, "trc": true, "nma": true, "ny": true, "zr": true,
	"lv": true, "gg": true, "afk": true, "pk": true, "boss": true, "acc": true, "tk": true, "sv": true,
}

// Key folds a message for fuzzy duplicate detection: lowercase, no diacritics, letters and digits only.
func Key(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		switch {
		case r == 'đ':
			b.WriteRune('d')
		case unicode.Is(unicode.Mn, r):
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}
