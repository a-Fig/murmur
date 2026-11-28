// Package termtext makes text another party chose safe to print on a terminal (#1332). A git
// remote chooses what git prints for it, a push's refusal reason and its "remote:" lines among
// them, and Murmur prints those in errors and reports. A terminal acts on what is in them: an
// escape sequence can clear a line, move the cursor, retitle the window, or make a hyperlink whose
// text says one address and whose target is another (OSC 8). Whether git masks them first depends
// on its version and settings, so Murmur does it itself.
package termtext

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Clean is s with every terminal escape sequence taken out whole, so no parameter of one is left
// to read as text, and every other character a terminal doesn't print as itself dropped: control
// characters, and format characters such as bidi overrides, which show text in another order than
// it reads. Line breaks, tabs and spaces stay. A carriage return alone becomes a line break, since one can
// put a line over the one before it. The printable rest, other scripts included, stays as it was.
func Clean(s string) string {
	if !needsCleaning(s) {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	b.Grow(len(s))
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == 0x1b: // ESC
			i = skipEscape(rs, i)
		case r == 0x9b: // CSI, as one C1 character
			i = skipCSI(rs, i+1)
		case r == 0x9d || r == 0x90 || r == 0x98 || r == 0x9e || r == 0x9f: // OSC, DCS, SOS, PM, APC
			i = skipString(rs, i+1)
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '\r':
			b.WriteByte('\n')
		case unicode.IsPrint(r) || unicode.Is(unicode.Zs, r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// needsCleaning says s has a character Clean would change, or bytes that aren't UTF-8, which Clean
// writes as U+FFFD: a terminal that takes a lone byte 0x9b for CSI would act on one.
func needsCleaning(s string) bool {
	return !utf8.ValidString(s) || strings.IndexFunc(s, func(r rune) bool {
		return r != '\n' && r != '\t' && !unicode.IsPrint(r) && !unicode.Is(unicode.Zs, r)
	}) >= 0
}

// skipEscape is the index of the last character of the escape sequence that starts at rs[i], ESC.
func skipEscape(rs []rune, i int) int {
	if i+1 >= len(rs) {
		return i
	}
	switch rs[i+1] {
	case '[': // CSI: ESC [ parameters, intermediates, a final byte
		return skipCSI(rs, i+2)
	case ']', 'P', 'X', '^', '_': // OSC, DCS, SOS, PM, APC: a string, to ST or BEL
		return skipString(rs, i+2)
	}
	// ESC, any intermediates (0x20 to 0x2f), then one final character: ESC c, ESC ( B, ESC 7.
	j := i + 1
	for j < len(rs) && rs[j] >= 0x20 && rs[j] <= 0x2f {
		j++
	}
	if j < len(rs) && rs[j] > 0x20 && rs[j] < 0x7f {
		return j
	}
	return j - 1
}

// skipCSI is the index of the final character of a control sequence whose parameters start at
// rs[j]: parameters and intermediates run from 0x20 to 0x3f, and the final character is 0x40 to
// 0x7e. One cut short by anything else ends before it, which is then read on its own.
func skipCSI(rs []rune, j int) int {
	for ; j < len(rs); j++ {
		switch r := rs[j]; {
		case r >= 0x40 && r <= 0x7e:
			return j
		case r < 0x20 || r > 0x3f:
			return j - 1
		}
	}
	return len(rs) - 1
}

// skipString is the index of the end of a control string whose text starts at rs[j]: BEL, ST as
// one C1 character, or ESC \. A line break also ends one, and stays: a string never ended would
// otherwise take every line after it, git's own among them. With no end, the rest of the text is
// the string's.
func skipString(rs []rune, j int) int {
	for ; j < len(rs); j++ {
		switch rs[j] {
		case 0x07, 0x9c:
			return j
		case 0x0a, 0x0d: // a line break
			return j - 1
		case 0x1b:
			if j+1 < len(rs) && rs[j+1] == '\\' {
				return j + 1
			}
			return j - 1 // another sequence starts here: the string ends before it
		}
	}
	return len(rs) - 1
}
