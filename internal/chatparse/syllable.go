package chatparse

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const (
	markGrave    = '\u0300'
	markAcute    = '\u0301'
	markCircum   = '\u0302'
	markTilde    = '\u0303'
	markBreve    = '\u0306'
	markHook     = '\u0309'
	markHorn     = '\u031B'
	markDotBelow = '\u0323'
)

var initials = []string{"ngh", "ng", "gh", "gi", "kh", "nh", "ph", "th", "tr", "ch", "qu",
	"b", "c", "d", "g", "h", "k", "l", "m", "n", "p", "r", "s", "t", "v", "x", ""}

var finals = []string{"ng", "nh", "ch", "c", "m", "n", "p", "t", ""}

// Vowel nuclei after folding marks (ă/â→a, ê→e, ô/ơ→o, ư→u) and dropping tones.
var nuclei = map[string]bool{
	"a": true, "e": true, "i": true, "o": true, "u": true, "y": true,
	"ai": true, "ao": true, "au": true, "ay": true, "eo": true, "eu": true, "ia": true, "ie": true,
	"iu": true, "oa": true, "oe": true, "oi": true, "oo": true, "ua": true, "ue": true, "ui": true,
	"uo": true, "uu": true, "uy": true, "ye": true,
	"ieu": true, "oai": true, "oay": true, "oao": true, "oeo": true, "uoi": true, "uou": true,
	"uya": true, "uye": true, "uyu": true, "yeu": true, "uay": true, "uai": true, "uau": true,
}

// Nuclei ending in a glide never take a final consonant.
var openOnly = map[string]bool{
	"ai": true, "ao": true, "au": true, "ay": true, "eo": true, "eu": true, "iu": true, "oi": true,
	"ui": true, "uu": true, "ia": true, "ieu": true, "oai": true, "oay": true, "oao": true,
	"oeo": true, "uoi": true, "uou": true, "yeu": true, "uay": true, "uai": true, "uau": true, "uyu": true, "uya": true,
}

// IsSyllable reports whether tok is a well-formed Vietnamese syllable (with or without diacritics).
func IsSyllable(tok string) bool {
	base, ok := fold(tok)
	if !ok || base == "" || len(base) > 7 {
		return false
	}
	for _, ini := range initials {
		if !strings.HasPrefix(base, ini) {
			continue
		}
		rest := base[len(ini):]
		if ini == "gi" && rest == "" {
			return true
		}
		for _, fin := range finals {
			if !strings.HasSuffix(rest, fin) {
				continue
			}
			nucleus := rest[:len(rest)-len(fin)]
			if nuclei[nucleus] && (fin == "" || !openOnly[nucleus]) {
				return true
			}
			if ini == "gi" && nucleus == "" && fin != "" {
				return true
			}
		}
	}
	return false
}

// fold lowercases, strips Vietnamese diacritics and rejects foreign marks or
// more than one tone mark.
func fold(tok string) (string, bool) {
	var b strings.Builder
	tones := 0
	for _, r := range norm.NFD.String(strings.ToLower(tok)) {
		switch {
		case r == 'đ':
			b.WriteByte('d')
		case r >= 'a' && r <= 'z':
			if r == 'f' || r == 'j' || r == 'w' || r == 'z' {
				return "", false
			}
			b.WriteRune(r)
		case r == markGrave || r == markAcute || r == markTilde || r == markHook || r == markDotBelow:
			tones++
		case r == markCircum || r == markBreve || r == markHorn:
		case unicode.Is(unicode.Mn, r):
			return "", false
		default:
			return "", false
		}
	}
	return b.String(), tones <= 1
}
