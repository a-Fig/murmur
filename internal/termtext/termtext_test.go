package termtext

import "testing"

func TestClean(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"plain text stays", "remote: main is protected\n\tsee the rules", "remote: main is protected\n\tsee the rules"},
		{"other scripts stay", "remote: ブランチは保護されています — ça marche", "remote: ブランチは保護されています — ça marche"},
		{"OSC 8 link, ended by ST", "remote: \x1b]8;;https://evil.example/\x1b\\https://github.com/a/b\x1b]8;;\x1b\\ done", "remote: https://github.com/a/b done"},
		{"OSC 8 link, ended by BEL", "\x1b]8;;https://evil.example/\x07click\x1b]8;;\x07", "click"},
		{"window title", "\x1b]0;pwned\x07remote: ok", "remote: ok"},
		{"clear line, cursor up", "remote: fine\n\x1b[1A\x1b[2Kremote: all good", "remote: fine\nremote: all good"},
		{"colour", "\x1b[31;1mrefused\x1b[0m", "refused"},
		{"private mode", "\x1b[?25lhidden cursor\x1b[?25h", "hidden cursor"},
		{"C1 CSI", "a\u009b2Kb", "ab"},
		{"C1 OSC to C1 ST", "a\u009d8;;https://evil.example/\u009cb", "ab"},
		{"DCS", "a\x1bPq#0;2;0;0;0\x1b\\b", "ab"},
		{"two-character escapes", "a\x1bcb\x1b(Bc\x1b7d", "abcd"},
		{"an OSC cut short ends at the next escape", "a\x1b]8;;x\x1b[2Kb", "ab"},
		{"an escape at the end", "a\x1b", "a"},
		{"a CSI at the end", "a\x1b[12", "a"},
		{"carriage return can't overwrite a line", "remote: real\rremote: fake", "remote: real\nremote: fake"},
		{"CRLF is one break", "a\r\nb", "a\nb"},
		{"other controls go", "a\x00b\x08c\x7fd\x0be", "abcde"},
		{"bidi override goes", "remote: ‮gnp.exe‬", "remote: gnp.exe"},
		{"no-break space stays", "a b", "a b"},
		{"bytes that aren't UTF-8", "a\x9b2Kb", "a�2Kb"},
	} {
		if got := Clean(c.in); got != c.want {
			t.Errorf("%s: Clean(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
		if again := Clean(c.want); again != c.want {
			t.Errorf("%s: Clean of its own output %q = %q", c.name, c.want, again)
		}
	}
}
