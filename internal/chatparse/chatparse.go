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
var timestampRe = regexp.MustCompile(`^\s*(\S{0,2}?)\s*[\[\(]?\s*([0-9IlOoDSZ]{1,2})\s*[:;.]\s*([0-9IlOoDSZ]{2,3})\s*[\]\)|Il1]?`)

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
var nameGroupRe = regexp.MustCompile(`^\s*[\[\(|Il]?([^\[\]\(\)]{1,32})\]`)

var digitFix = strings.NewReplacer("I", "1", "l", "1", "O", "0", "o", "0", "D", "0", "S", "5", "Z", "2")

// A second header inside one OCR line (lines merged by OCR), e.g. "… | *l14:1Z] [Name] …".
var midHeaderRe = regexp.MustCompile(`\s(?:\|\s*)?([*!@#$%^]?[\[(]?[|Il]?[0-9IlOoDSZ]{2}\s*[:;]\s*[0-9IlOoDSZ]{2}\s*[\])])`)

// Ace Spy's own log ("14:16:43 [ANI] …") read back by OCR looks like "[14:16] 43 [ANI] …".
var logEchoRe = regexp.MustCompile(`^\d{1,2}\s*\[`)

// splitMerged splits a line wherever another message header starts.
func splitMerged(line string) []string {
	var parts []string
	start := 0
	for _, m := range midHeaderRe.FindAllStringSubmatchIndex(line, -1) {
		if m[2] <= start {
			continue
		}
		parts = append(parts, strings.TrimRight(strings.TrimSpace(line[start:m[2]]), " |"))
		start = m[2]
	}
	return append(parts, strings.TrimSpace(line[start:]))
}

// Messages assembles visible OCR lines (top to bottom) into meaningful messages.
// Only text that starts with a "[hh:mm]" header (plus its wrapped lines) is kept.
func Messages(lines []string) []string {
	msgs := assemble(lines)
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.String()
	}
	return out
}

func assemble(lines []string) []*message { return assembleColored(lines, nil) }

// assembleColored is assemble with per-token colours of each input line
// (colors[i] aligned with strings.Fields of lines[i]; nil = unknown). Each message
// collects the colours of every line it was built from.
func assembleColored(lines []string, colors [][]string) []*message {
	var out []*message
	var cur *message
	flush := func() {
		if cur != nil && cur.meaningful() {
			cur.body = fixNumbers(cur.body)
			out = append(out, cur)
		}
		cur = nil
	}
	var split []string
	var splitColors [][]string
	for i, raw := range lines {
		parts := splitMerged(clean(raw))
		split = append(split, parts...)
		var lc []string
		if i < len(colors) {
			lc = colors[i]
		}
		splitColors = append(splitColors, partColors(clean(raw), parts, lc)...)
	}
	for i, line := range split {
		if line == "" {
			continue
		}
		if m, ok := parseHeader(line); ok {
			flush()
			cur = m
			cur.colors = splitColors[i]
			if cur.colors != nil {
				cur.src = strings.Fields(line)
			}
			continue
		}
		if cur != nil {
			if isMeaningfulLine(line) {
				cur.body = strings.TrimSpace(cur.body + " " + line)
				if cur.colors != nil && splitColors[i] != nil {
					cur.colors = append(append([]string(nil), cur.colors...), splitColors[i]...)
					cur.src = append(append([]string(nil), cur.src...), strings.Fields(line)...)
				} else {
					cur.colors, cur.src = nil, nil
				}
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
	colors  []string // token colours of the source line(s), in order
	src     []string // the source tokens those colours belong to
}

// partColors splits a line's token colours across the parts splitMerged made of it.
func partColors(line string, parts []string, lc []string) [][]string {
	out := make([][]string, len(parts))
	toks := strings.Fields(line)
	if len(lc) != len(toks) {
		return out
	}
	j := 0
	for pi, p := range parts {
		var pc []string
		for _, t := range strings.Fields(p) {
			for j < len(toks) && toks[j] != t {
				j++
			}
			if j == len(toks) {
				pc = nil
				break
			}
			pc = append(pc, lc[j])
			j++
		}
		out[pi] = pc
	}
	return out
}

// Msg is an assembled chat message with the colour of each of its trailing tokens:
// Colors[i] belongs to the i-th of the last len(Colors) tokens of strings.Fields(Text).
// The header ("^[hh:mm]") token never gets a colour.
type Msg struct {
	Text   string
	Colors []string
}

var timeTokenRe = regexp.MustCompile(`\d\s*[:;.]\s*\d`)

// colored aligns the source colours with the tokens of m.String(). The body is a
// suffix of the source, so it takes the source's trailing colours. Name groups take
// the colours of the source tokens after the timestamp; a name cut out of a token
// glued to the body (e.g. "[BC] [Item][Item]") takes the body's first colour.
func (m *message) colored() Msg {
	text := m.String()
	if len(m.colors) == 0 || len(m.colors) != len(m.src) {
		return Msg{Text: text}
	}
	bodyToks := len(strings.Fields(m.body))
	if bodyToks > len(m.colors) {
		return Msg{Text: text}
	}
	bodyColors := m.colors[len(m.colors)-bodyToks:]
	head := m.colors[:len(m.colors)-bodyToks]
	start := 0
	for i, tok := range m.src[:len(head)] {
		if timeTokenRe.MatchString(tok) {
			start = i + 1
			break
		}
	}
	head = head[start:]
	var nameColors []string
	k := 0
	for _, n := range m.names {
		for range strings.Fields("[" + n + "]") {
			switch {
			case k < len(head):
				nameColors = append(nameColors, head[k])
			case len(bodyColors) > 0:
				nameColors = append(nameColors, bodyColors[0])
			default:
				nameColors = append(nameColors, "")
			}
			k++
		}
	}
	all := append(nameColors, bodyColors...)
	if len(all) != len(strings.Fields(text))-1 {
		return Msg{Text: text, Colors: bodyColors}
	}
	return Msg{Text: text, Colors: all}
}

// MessagesColored is Messages with token colours (see Msg).
func MessagesColored(lines []string, colors [][]string) []Msg {
	msgs := assembleColored(lines, colors)
	out := make([]Msg, len(msgs))
	for i, m := range msgs {
		out[i] = m.colored()
	}
	return out
}

// Inside numbers the game font's 0 is often read as D or O ("30D", "4Dk").
var zeroLikeRe = regexp.MustCompile(`\b\d+[DOo][\dDOo]*(?:k|m|tr)?\b|\b\d*[DOo]\d+[\dDOo]*(?:k|m|tr)?\b`)

func fixNumbers(s string) string {
	return zeroLikeRe.ReplaceAllStringFunc(s, func(tok string) string {
		return strings.NewReplacer("D", "0", "O", "0", "o", "0").Replace(tok)
	})
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

// meaningful accepts a message with a well-formed header ("[hh:mm] [Name]") whose body
// is real chat rather than OCR noise. The header is the strong signal, so the body may be
// teencode, abbreviations or emoticons ("=))", ":V", "cmndt").
func (m *message) meaningful() bool {
	if len(m.names) == 0 || m.body == "" {
		return false
	}
	if strings.HasPrefix(m.body, "[") && !strings.Contains(m.body, "]") || logEchoRe.MatchString(m.body) {
		return false // unclosed name bracket, or our own log read back
	}
	st := score(m.body)
	if st.junkRatio() > 0.2 {
		return false
	}
	return st.words < 3 || st.singleRatio() <= 0.7
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
	m.body = strings.TrimLeft(strings.TrimSpace(rest), ".,|`'\"")
	m.body = strings.TrimSpace(m.body)
	// A message that is only an item link ("[Tower Defender S-VI]") parses as an
	// extra name group; the last group is then the content.
	if m.body == "" && len(m.names) >= 2 {
		m.body = "[" + m.names[len(m.names)-1] + "]"
		m.names = m.names[:len(m.names)-1]
	}
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
	line := clean(s)
	if len(splitMerged(line)) != 1 {
		return false
	}
	m, ok := parseHeader(line)
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

// Includes ` ~ ^ which players use to type tone marks in plain text ("du`", "go~").
const allowedPunct = ".,!?:;'\"()[]+-/%&@#*=~_<>`^{}|$\\"

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
