// Package scrub blocks secrets from reaching the bus. Everything sent through Murmur stays in the
// repo's history, encrypted for the team's devices, or in the clear from a daemon older than
// encryption, so a match blocks the send instead of being redacted: the agent learns what happened
// and can send again without it.
//
// The list is short and Murmur's own (docs/VISION.md, "Leaked secrets"). It aims at things agents
// actually paste: API keys, tokens, private keys, credentials in URLs and .env lines.
package scrub

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
)

type pattern struct {
	name string
	re   *regexp.Regexp
	// hints are text every match of re has one of, looked for before re is run, as most texts have
	// none and a regexp is slow over a large file (#981). None: re is always run.
	hints []string
	// secret, when set, decides whether a match is one: re finds the shape, and secret tells a
	// credential in it from prose or a placeholder in the same shape. It gets re's submatches.
	secret func(m []string) bool
}

// at starts a key whose prefix no identifier has (`sk-ant-`, `ghp_`, `ya29.`): the start of the
// text or any character but a letter or a digit. `\b` would miss such a key right after an
// underscore (`my_sk-ant-…`), since an underscore is a word character. A prefix an identifier can
// end in keeps `\b`: `load_hf_AutoModelFor…` isn't a Hugging Face token, nor
// `ZONE_ASIAPACIFICSOUTHEAST` an AWS key.
const at = `(?:^|[^A-Za-z0-9])`

// patterns are tried in order and the first match names the error, so a more specific kind has
// to come before a broader pattern that also matches it. An OpenRouter key (`sk-or-v1-` and 64
// hex characters) is such a case: the broad OpenAI-style pattern matches it too, and would have
// the error call it an OpenAI key. The OpenRouter pattern therefore sits above that one, the
// same way the Anthropic pattern already does.
var patterns = []pattern{
	{name: "a private key", hints: []string{"PRIVATE KEY"}, re: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY( BLOCK)?-----`)}, // PEM, and PGP's BLOCK
	{name: "an AWS access key", hints: []string{"AKIA", "ASIA"}, re: regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{name: "a GitHub token", hints: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"}, re: regexp.MustCompile(at + `(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}|` + at + `github_pat_[A-Za-z0-9_]{50,}`)},
	{name: "a GitLab personal access token", hints: []string{"glpat-"}, re: regexp.MustCompile(at + `glpat-[A-Za-z0-9_-]{20,}`)},
	{name: "an npm token", hints: []string{"npm_"}, re: regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}`)},
	{name: "a Hugging Face token", hints: []string{"hf_"}, re: regexp.MustCompile(`\bhf_[A-Za-z]{34,}`)},
	{name: "an Anthropic API key", hints: []string{"sk-ant-"}, re: regexp.MustCompile(at + `sk-ant-[A-Za-z0-9_-]{20,}`)},
	{name: "an OpenRouter API key", hints: []string{"sk-or-v1-"}, re: regexp.MustCompile(at + `sk-or-v1-[0-9a-fA-F]{64}`)},
	{name: "an OpenAI-style API key", hints: []string{"sk-"}, re: regexp.MustCompile(at + `sk-(proj|svcacct)-[A-Za-z0-9_-]{32,}|\bsk-[A-Za-z0-9_-]{32,}`)},
	{name: "a Stripe key", hints: []string{"k_live_", "k_test_"}, re: regexp.MustCompile(at + `[rs]k_(live|test)_[A-Za-z0-9]{20,}`)},
	{name: "a Slack token", hints: []string{"xox"}, re: regexp.MustCompile(at + `xox[abposr]-[A-Za-z0-9-]{10,}`)},
	// Slack's own docs, and many .env.example files, show T00000000/B00000000/XXXX…; a real
	// webhook's last part is random, letters and digits both.
	{name: "a Slack webhook URL", hints: []string{"hooks.slack.com/services/"}, re: regexp.MustCompile(`hooks\.slack\.com/services/T[A-Z0-9]+/B[A-Z0-9]+/([A-Za-z0-9]{16,})`),
		secret: func(m []string) bool { return lettersAndDigits(m[1]) }},
	{name: "a Google API key", hints: []string{"AIza"}, re: regexp.MustCompile(at + `AIza[0-9A-Za-z_-]{35}`)},
	{name: "a Google OAuth token", hints: []string{"ya29."}, re: regexp.MustCompile(at + `ya29\.[0-9A-Za-z_-]{20,}`)},
	{name: "an Azure storage account key", hints: []string{"AccountKey="}, re: regexp.MustCompile(`AccountKey=[A-Za-z0-9+/]{40,}`)},
	{name: "a JSON web token", hints: []string{"eyJ"}, re: regexp.MustCompile(at + `eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)},
	// A bearer token is opaque, so the word alone says nothing: "Bearer YOUR_TOKEN_HERE" or a
	// long word in prose passes, and only a token mixing letters and digits is one.
	{name: "a bearer token", re: regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/-]{20,}=*)`),
		secret: func(m []string) bool { return lettersAndDigits(m[1]) }},
	{name: "a password in a URL", hints: []string{"://"}, re: regexp.MustCompile(`\b[a-z][a-z0-9+.-]*://[^\s:/@]+:[^\s@/]{3,}@`)},
	// A password with a slash in it, which the pattern above stops at. A port and a path
	// (`localhost:5173/@vite/client`) has the same shape, so what follows the last colon before the
	// slash mustn't be a number, the part before the @ mustn't end a path, the password has to mix
	// letters and digits, and an image digest (`…@sha256:…`) isn't a host. A user name never starts
	// with `[`, as an IPv6 host does, with colons of its own: neither `[::1]/…` nor `[::1]:8000/…`
	// is a user and a password.
	{name: "a password in a URL", hints: []string{"://"}, re: regexp.MustCompile(`\b[a-z][a-z0-9+.-]*://[^\s:/@\[]+:([^\s@/]+)(/[^\s@]*)@([A-Za-z0-9]+)`),
		secret: func(m []string) bool {
			port := m[1][strings.LastIndexByte(m[1], ':')+1:]
			return strings.Trim(port, "0123456789") != "" && !strings.HasSuffix(m[2], "/") &&
				lettersAndDigits(m[1]+m[2]) && m[3] != "sha256" && m[3] != "sha512"
		}},
}

// assignment finds NAME=value lines whose name says secret. The value is checked separately
// (see looksSecret), so that `API_KEY = process.env.API_KEY` in a code discussion passes. It
// matches the name and the sign only, its submatch the name: the value is read after it (value),
// so an assignment inside another's quoted value, `"note": "set API_KEY=… here"`, is found too.
var assignment = regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:SECRET|TOKEN|PASSWORD|PASSWD|PASSPHRASE|API_?KEY|PRIVATE_?KEY|ACCESS_?KEY)[A-Z0-9_]*)["']?\s*[:=]\s*`)

// passwordName is a name whose quoted value may be a passphrase, words with spaces between them,
// all of which is the secret (#948). Only its first word used to be checked, so
// "purple monkey 42 dishwasher" passed, and stayed in the bus's history. The name ends in the
// word, as `db_password` and `SMTP_PASSWD2` do: `passwordExpired` or `password_hint` holds text
// about a password ("Expired 30 days ago"), not one, and so do a form's own `reset_password` or
// `forgot_password` ("Link expires in 24 hours"), named by that alone: `ADMIN_RESET_PASSWORD` is
// a config's password, and `change_password` is often the field a new one is sent in.
var (
	passwordName = regexp.MustCompile(`(?i)PASS(WORD|WD|PHRASE)_?[0-9]*$`)
	passwordForm = regexp.MustCompile(`(?i)^(RESET|FORGOT|FORGOTTEN|ONE_?TIME)_?PASS(WORD|WD)$`)
)

// publicKeyToken is .NET's name for the public identity of a signed assembly
// (publicKeyToken="6595b64144ccf1df"), in every Windows program's manifest and in .NET config
// files: public, so not a credential, though its name says token (#737). It is always 16 hex
// digits (hex16), so any other value under that name is still checked.
var (
	publicKeyToken = regexp.MustCompile(`(?i)^publicKeyToken\b`)
	hex16          = regexp.MustCompile(`^[0-9A-Fa-f]{16}$`)
)

// Check returns an error naming the kind of secret if any text looks like one. Each text is
// checked as written, and then as JSON would read it (decodedLines).
func Check(texts ...string) error {
	for _, t := range texts {
		for d, level := t, 0; d != "" && level <= maxDecodes; level++ {
			if level > 0 {
				if d = decodedLines(d); d == "" {
					break
				}
			}
			if name := format(d); name != "" {
				return blocked(name)
			}
			if assigned(d) {
				return blocked("a credential assignment")
			}
		}
	}
	return nil
}

// assigned says whether t holds a credential assignment.
func assigned(t string) bool {
	for _, m := range assignment.FindAllStringSubmatchIndex(t, -1) {
		if credential(t[m[2]:m[3]], t[m[1]:]) {
			return true
		}
	}
	return false
}

// format names the first of patterns that finds a secret in t, or is "".
func format(t string) string {
	for _, p := range patterns {
		if p.hints != nil && !slices.ContainsFunc(p.hints, func(h string) bool { return strings.Contains(t, h) }) {
			continue
		}
		if p.secret == nil {
			if p.re.MatchString(t) {
				return p.name
			}
			continue
		}
		for _, m := range p.re.FindAllStringSubmatch(t, -1) {
			if p.secret(m) {
				return p.name
			}
		}
	}
	return ""
}

// maxDecodes is how many times Check decodes a text: a JSON string of JSON escapes its escapes
// again, as an agent's tool call that carries a serialized config does.
const maxDecodes = 3

// decodedLines is the lines of t that have a backslash, with JSON's backslash escapes decoded
// (jsonUnescape), or "" if none changes. Check looks for secrets in it, and decodes it again for
// each level of JSON within JSON (#981): a key in a JSON string can be written with escapes in it
// (a webhook URL's slashes as \/, a letter as \u0067), or with an escape just before it (\n),
// which the formats, reading the text as written, missed; and a credential assignment in JSON
// within JSON has \" around its name. The lines are decoded whole, not each quoted string in them:
// in prose, logs or escaped JSON the quotes don't pair reliably, and decoding changes only what
// follows a backslash. A line without one is the same decoded, and is left out but for the line
// before one that has, so a large file with few backslashes costs little more to check than before.
func decodedLines(t string) string {
	var b strings.Builder
	changed := false
	prev, next := "", 0 // the line before, and the index of the line after the last one kept
	i := 0
	for line := range strings.Lines(t) {
		line = strings.TrimSuffix(line, "\n")
		if strings.Contains(line, `\`) {
			// The line before goes too, as what a match starts with can be there ("Bearer", a
			// name). A gap where lines were left out is marked with a character no pattern runs
			// over, so the lines on either side of it don't read as one.
			if i > 0 && next < i {
				if next < i-1 && b.Len() > 0 {
					b.WriteString("\x00\n")
				}
				b.WriteString(prev + "\n")
			}
			d := jsonUnescape(line)
			changed = changed || d != line
			b.WriteString(d + "\n")
			next = i + 1
		}
		prev = line
		i++
	}
	if !changed {
		return ""
	}
	return b.String()
}

func blocked(what string) error {
	return fmt.Errorf("not sent: this contains what looks like %s. Everything sent through the bus "+
		"stays in the repo's history; remove it and send again", what)
}

// credential says whether rest, the text after an assignment's sign, starts with a credential
// assigned to name. Its first word, up to a space or a quote, after an opening quote if there is
// one, is looksSecret's to judge (publicKeyToken's 16 hex digits aside). Under a password's name a
// quoted value with spaces in it is judged whole too, as a passphrase (looksPassphrase). A quoted
// value is judged as quotedValue reads it, with JSON's escapes decoded.
func credential(name, rest string) bool {
	v, quoted := quotedValue(rest)
	first := v
	if !quoted {
		first = strings.TrimLeft(rest, `"'`)
		if len(rest)-len(first) > 1 {
			first = rest[1:] // only one quote opens a value
		}
	}
	if i := strings.IndexFunc(first, func(r rune) bool { return unicode.IsSpace(r) || r == '"' || r == '\'' }); i >= 0 {
		first = first[:i]
	}
	if looksSecret(first) && !(publicKeyToken.MatchString(name) && hex16.MatchString(first)) {
		return true
	}
	return quoted && strings.IndexFunc(v, unicode.IsSpace) >= 0 && passwordName.MatchString(name) && !passwordForm.MatchString(name) && looksPassphrase(v)
}

// quotedValue reads the quoted string rest starts with, closed on its line: in double quotes, with
// JSON's backslash escapes decoded (jsonUnescape), or in single quotes, where YAML writes a quote as
// two.
func quotedValue(rest string) (string, bool) {
	if rest == "" || (rest[0] != '"' && rest[0] != '\'') {
		return "", false
	}
	q := rest[0]
	for j := 1; j < len(rest); j++ {
		switch c := rest[j]; {
		case c == '\n':
			return "", false
		case q == '"' && c == '\\':
			j++
		case c == q && q == '\'' && j > 1 && j+1 < len(rest) && rest[j+1] == '\'':
			j++ // '' inside a single-quoted value
		case c == q && q == '"':
			return jsonUnescape(rest[1:j]), true
		case c == q:
			return rest[1:j], true
		}
	}
	return "", false
}

// jsonUnescape decodes JSON's backslash escapes in s, a double-quoted value's text, so the value is
// judged by what it says: a serialized config or tool call writes a passphrase's tab as \t, and its
// backslash, read as written, marked the value as code and let it through (#967). An escape JSON
// doesn't have is kept as written.
func jsonUnescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch e := s[i+1]; e {
		case '"', '\\', '/':
			b.WriteByte(e)
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'u':
			r, n := jsonRune(s[i:])
			if n == 0 {
				b.WriteByte('\\')
				continue
			}
			b.WriteRune(r)
			i += n - 1
			continue
		default:
			b.WriteByte('\\')
			continue
		}
		i++
	}
	return b.String()
}

// jsonRune reads the \uXXXX escape s starts with, with the low half after it of a surrogate pair,
// and returns the rune and the bytes it read: 0 for an escape that isn't one.
func jsonRune(s string) (rune, int) {
	hex := func(s string) (rune, bool) {
		if len(s) < 6 || s[0] != '\\' || s[1] != 'u' {
			return 0, false
		}
		n, err := strconv.ParseUint(s[2:6], 16, 32)
		return rune(n), err == nil
	}
	r, ok := hex(s)
	if !ok {
		return 0, 0
	}
	if utf16.IsSurrogate(r) {
		if lo, ok := hex(s[6:]); ok {
			if pair := utf16.DecodeRune(r, lo); pair != unicode.ReplacementChar {
				return pair, 12
			}
		}
	}
	return r, 6
}

// looksPassphrase says whether a password's quoted value of several words is a passphrase rather
// than prose or a placeholder. It holds to looksSecret's rules, with spaces allowed: 12 characters
// or more, letters mixed with digits, and none of the characters of code or a placeholder ({, <,
// (, % or a $ that starts a word and a variable: ${, $(, $name, $1). A $ inside a word stands for
// an s (Pa$$w0rd, Ca$h). So a passphrase of words alone passes, as
// `PASSWORD=correcthorsebatterystaple` does. Prose under a password's own name is a form's
// validation message ("Must be at least 12 characters"), with one of formWords, which a
// passphrase rarely has, or a note in its place ("TODO set in prod v2"), with two of noteWords:
// each of those is an everyday word ("Max Power 2019 rocket"), and only two of them together
// read as a note.
func looksPassphrase(v string) bool {
	if len(v) < 12 || !lettersAndDigits(v) {
		return false
	}
	prev := ' '
	for i, r := range v {
		if r == '$' && wordStart(prev) && i+1 < len(v) && variableStart.MatchString(v[i+1:]) {
			return false
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsSpace(r) && !strings.ContainsRune("-_+/=.~!@#?,;:&*^$'", r) {
			return false
		}
		prev = r
	}
	notes := 0
	for _, w := range strings.FieldsFunc(strings.ToLower(v), func(r rune) bool { return !unicode.IsLetter(r) }) {
		if formWords[w] {
			return false
		}
		if noteWords[w] {
			notes++
		}
	}
	return notes < 2
}

// wordStart says a character before a $ leaves it at the start of a word: a space, or punctuation
// a variable follows (`=$HOME`, `"$1"`), but not a letter, a digit or another $.
func wordStart(prev rune) bool {
	return unicode.IsSpace(prev) || strings.ContainsRune(`"'=:,;/`, prev)
}

// variableStart is what follows a $ that starts a shell or template variable.
var variableStart = regexp.MustCompile(`^[{(A-Za-z0-9_]`)

// formWords are words of a form's text about a password that a passphrase rarely has, one of
// which marks a value as that text. "Characters" is here in a few languages besides English, for
// an agent working on a form's translations. noteWords are words of a note left in a password's
// place, or a form's status text, two of which mark a value as one.
var formWords, noteWords = map[string]bool{}, map[string]bool{}

func init() {
	for _, w := range strings.Fields(`must least characters chars char digit digits letter letters
		number numbers symbol symbols enter invalid incorrect required minimum maximum allowed your
		example password passwords passphrase zeichen caracteres caractères caratteri tekens`) {
		formWords[w] = true
	}
	for _, w := range strings.Fields(`set same see not yet short weak strong strength min max more add
		todo tbd redacted stored vault step below ticket staging prod production dev changed expires
		expired sent ago days hours minutes seconds`) {
		noteWords[w] = true
	}
}

// looksSecret says whether an assigned value looks like a real credential rather than code or
// a placeholder: long, token-shaped, and mixing letters with digits. A password's own punctuation,
// ! @ #, counts as token-shaped. & * % don't: they run a value into the next parameter of a query
// string (token_type=bearer&expires_in=3600), mask a key (sk-****1234), or name a Windows
// variable (%API_KEY%). With @ and # in, an email address (PASSWORD_RESET_FROM=noreply2026@…) and a
// hex color (TOKEN_COLOR=#a1b2c3) would read as passwords, so those two shapes pass.
func looksSecret(v string) bool {
	if len(v) < 12 || emailOrColor.MatchString(v) {
		return false
	}
	for _, r := range v {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("-_+/=.~!@#", r) {
			return false // parentheses, $, <, {: code or a placeholder
		}
	}
	return lettersAndDigits(v)
}

var emailOrColor = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}$|^#[0-9A-Fa-f]+$`)

// lettersAndDigits reports whether s has both, as a key does and a word or a placeholder doesn't.
func lettersAndDigits(s string) bool {
	return strings.IndexFunc(s, unicode.IsLetter) >= 0 && strings.IndexFunc(s, unicode.IsDigit) >= 0
}
