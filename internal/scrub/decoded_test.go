package scrub

import (
	"strings"
	"testing"
)

// A key in a JSON string, written with JSON's escapes, is found as the formats find it written out
// (#981): the format checks read only the text as written, so escaped slashes, a \u escape or a
// \n just before the key let it through under a field of any name.
func TestBlocksFormatsWrittenWithJSONEscapes(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{`{"url": "https:\/\/hooks.slack.com\/services\/` + `T0123ABCD\/B0456EFGH\/` + `abcdEFGHijklMNOPqrst1234"}`, "a Slack webhook URL"},
		{`{"db": "postgres:\/\/app:` + `hunter2pass` + `@db.example.com\/app"}`, "a password in a URL"},
		{`{"note": "use this:\n` + `sk-ant-` + `api03-abcdefghijklmnopqrstuvwxyz"}`, "an Anthropic API key"},
		{`{"note": "key\t` + `ghp_` + `abcdefghijklmnopqrstuvwxyz0123456789AB"}`, "a GitHub token"},
		{`{"v": "\u0067hp_` + `abcdefghijklmnopqrstuvwxyz0123456789AB"}`, "a GitHub token"},
		{`{"pem": "\n-----BEGIN RSA PRIVATE KEY-----\nMIIE"}`, "a private key"},
		{`{"args": "{\"url\": \"https:\\\/\\\/hooks.slack.com\\\/services\\\/` + `T0123ABCD\\\/B0456EFGH\\\/` + `abcdEFGHijklMNOPqrst1234\"}"}`, "a Slack webhook URL"}, // JSON within JSON
	} {
		err := Check(c.text)
		if err == nil {
			t.Errorf("not blocked: %q", c.text)
		} else if !strings.Contains(err.Error(), "looks like "+c.want+".") {
			t.Errorf("%q: want %q: %v", c.text, c.want, err)
		}
	}
}

// Backslashes in ordinary text, decoded, still make nothing that looks like a secret.
func TestPassesTextWithBackslashes(t *testing.T) {
	for _, s := range []string{
		`{"path": "C:\\Users\\tyler\\murmur\\internal\\scrub"}`,
		`the pattern is \bsk-[A-Za-z0-9_-]{32,}, see scrub.go`,
		`{"url": "https:\/\/hooks.slack.com\/services\/T00000000\/B00000000\/XXXXXXXXXXXXXXXXXXXXXXXX"}`, // Slack's docs
		`{"url": "http:\/\/localhost:5173\/@vite\/client"}`,
		`{"url": "https:\/\/github.com\/a-Fig\/murmur\/pull\/7"}`,
		`printf "line one\nline two\n"`,
		`{"msg": "Authorization: Bearer \u003ctoken\u003e"}`,
		`{"image": "ghcr.io\/a-fig\/murmur@sha256:0123abcd"}`,
		`a lone backslash at the end \`,
	} {
		if err := Check(s); err != nil {
			t.Errorf("blocked %q: %v", s, err)
		}
	}
}
