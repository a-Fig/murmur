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
	"strings"
	"unicode"
)

type pattern struct {
	name string
	re   *regexp.Regexp
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
	{name: "a private key", re: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY( BLOCK)?-----`)}, // PEM, and PGP's BLOCK
	{name: "an AWS access key", re: regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{name: "a GitHub token", re: regexp.MustCompile(at + `(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}|` + at + `github_pat_[A-Za-z0-9_]{50,}`)},
	{name: "a GitLab personal access token", re: regexp.MustCompile(at + `glpat-[A-Za-z0-9_-]{20,}`)},
	{name: "an npm token", re: regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}`)},
	{name: "a Hugging Face token", re: regexp.MustCompile(`\bhf_[A-Za-z]{34,}`)},
	{name: "an Anthropic API key", re: regexp.MustCompile(at + `sk-ant-[A-Za-z0-9_-]{20,}`)},
	{name: "an OpenRouter API key", re: regexp.MustCompile(at + `sk-or-v1-[0-9a-fA-F]{64}`)},
	{name: "an OpenAI-style API key", re: regexp.MustCompile(at + `sk-(proj|svcacct)-[A-Za-z0-9_-]{32,}|\bsk-[A-Za-z0-9_-]{32,}`)},
	{name: "a Stripe key", re: regexp.MustCompile(at + `[rs]k_(live|test)_[A-Za-z0-9]{20,}`)},
	{name: "a Slack token", re: regexp.MustCompile(at + `xox[abposr]-[A-Za-z0-9-]{10,}`)},
	// Slack's own docs, and many .env.example files, show T00000000/B00000000/XXXX…; a real
	// webhook's last part is random, letters and digits both.
	{name: "a Slack webhook URL", re: regexp.MustCompile(`hooks\.slack\.com/services/T[A-Z0-9]+/B[A-Z0-9]+/([A-Za-z0-9]{16,})`),
		secret: func(m []string) bool { return lettersAndDigits(m[1]) }},
	{name: "a Google API key", re: regexp.MustCompile(at + `AIza[0-9A-Za-z_-]{35}`)},
	{name: "a Google OAuth token", re: regexp.MustCompile(at + `ya29\.[0-9A-Za-z_-]{20,}`)},
	{name: "an Azure storage account key", re: regexp.MustCompile(`AccountKey=[A-Za-z0-9+/]{40,}`)},
	{name: "a JSON web token", re: regexp.MustCompile(at + `eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)},
	// A bearer token is opaque, so the word alone says nothing: "Bearer YOUR_TOKEN_HERE" or a
	// long word in prose passes, and only a token mixing letters and digits is one.
	{name: "a bearer token", re: regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/-]{20,}=*)`),
		secret: func(m []string) bool { return lettersAndDigits(m[1]) }},
	{name: "a password in a URL", re: regexp.MustCompile(`\b[a-z][a-z0-9+.-]*://[^\s:/@]+:[^\s@/]{3,}@`)},
	// A password with a slash in it, which the pattern above stops at. A port and a path
	// (`localhost:5173/@vite/client`) has the same shape, so what follows the last colon before the
	// slash mustn't be a number, the part before the @ mustn't end a path, the password has to mix
	// letters and digits, and an image digest (`…@sha256:…`) isn't a host. A user name never starts
	// with `[`, as an IPv6 host does, with colons of its own: neither `[::1]/…` nor `[::1]:8000/…`
	// is a user and a password.
	{name: "a password in a URL", re: regexp.MustCompile(`\b[a-z][a-z0-9+.-]*://[^\s:/@\[]+:([^\s@/]+)(/[^\s@]*)@([A-Za-z0-9]+)`),
		secret: func(m []string) bool {
			port := m[1][strings.LastIndexByte(m[1], ':')+1:]
			return strings.Trim(port, "0123456789") != "" && !strings.HasSuffix(m[2], "/") &&
				lettersAndDigits(m[1]+m[2]) && m[3] != "sha256" && m[3] != "sha512"
		}},
}

// assignment finds NAME=value lines whose name says secret. The value is checked separately
// (see looksSecret), so that `API_KEY = process.env.API_KEY` in a code discussion passes. Its
// submatches are the name, then a value in double quotes (JSON's escapes allowed), one in single
// quotes, or a bare one, which ends at a space or a quote, as an unclosed quoted one does.
var assignment = regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:SECRET|TOKEN|PASSWORD|PASSWD|PASSPHRASE|API_?KEY|PRIVATE_?KEY|ACCESS_?KEY)[A-Z0-9_]*)["']?\s*[:=]\s*(?:"((?:[^"\\\n]|\\.)*)"|'([^'\n]*)'|["']?([^\s"']+))`)

// passwordName is a name whose quoted value may be a passphrase, words with spaces between them,
// all of which is the secret (#948). Only its first word used to be checked, so
// "purple monkey 42 dishwasher" passed, and stayed in the bus's history.
var passwordName = regexp.MustCompile(`(?i)PASS(WORD|WD|PHRASE)`)

// publicKeyToken is .NET's name for the public identity of a signed assembly
// (publicKeyToken="6595b64144ccf1df"), in every Windows program's manifest and in .NET config
// files: public, so not a credential, though its name says token (#737). It is always 16 hex
// digits (hex16), so any other value under that name is still checked.
var (
	publicKeyToken = regexp.MustCompile(`(?i)^publicKeyToken\b`)
	hex16          = regexp.MustCompile(`^[0-9A-Fa-f]{16}$`)
)

// Check returns an error naming the kind of secret if any text looks like one.
func Check(texts ...string) error {
	for _, t := range texts {
		for _, p := range patterns {
			if p.secret == nil {
				if p.re.MatchString(t) {
					return blocked(p.name)
				}
				continue
			}
			for _, m := range p.re.FindAllStringSubmatch(t, -1) {
				if p.secret(m) {
					return blocked(p.name)
				}
			}
		}
		for _, m := range assignment.FindAllStringSubmatch(t, -1) {
			value, quoted := m[2]+m[3]+m[4], m[4] == ""
			if credential(m[1], value, quoted) && !(publicKeyToken.MatchString(m[0]) && hex16.MatchString(value)) {
				return blocked("a credential assignment")
			}
		}
	}
	return nil
}

func blocked(what string) error {
	return fmt.Errorf("not sent: this contains what looks like %s. Everything sent through the bus "+
		"stays in the repo's history; remove it and send again", what)
}

// credential says whether the value assigned to name is one. A bare value, or a quoted one without
// a space, is looksSecret's to judge, as is the first word of a quoted one with spaces. Under a
// password's name the whole of a quoted one is judged too, as a passphrase (looksPassphrase).
func credential(name, value string, quoted bool) bool {
	first, spaced := value, false
	if i := strings.IndexFunc(value, unicode.IsSpace); i >= 0 {
		first, spaced = value[:i], true
	}
	if looksSecret(first) {
		return true
	}
	return quoted && spaced && passwordName.MatchString(name) && looksPassphrase(value)
}

// looksPassphrase says whether a password's quoted value of several words is a passphrase rather
// than prose or a placeholder. It holds to looksSecret's rules, with spaces allowed: 12 characters
// or more, letters mixed with digits, and none of the characters of code or a placeholder ($, {,
// <, ( or %). So a passphrase of words alone passes, as `PASSWORD=correcthorsebatterystaple` does.
// Prose under a password's name is a form's label or its validation message ("Must be at least 12
// characters", "Enter your password"), and it has a word a passphrase rarely does: one of
// proseWords.
func looksPassphrase(v string) bool {
	if len(v) < 12 || !lettersAndDigits(v) {
		return false
	}
	for _, r := range v {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(" -_+/=.~!@#?,&*^", r) {
			return false
		}
	}
	for _, w := range strings.FieldsFunc(strings.ToLower(v), func(r rune) bool { return !unicode.IsLetter(r) }) {
		if proseWords[w] {
			return false
		}
	}
	return true
}

// proseWords are words of a form's text about a password, which mark a value as that text.
var proseWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the is are be at least of to in for with and or not no
		your enter type use must should can characters chars char digits digit number numbers letters
		symbols minimum min maximum max length long required invalid incorrect wrong here example
		placeholder password passwords passphrase passwd secret token key`) {
		proseWords[w] = true
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
