package scrub

import (
	"strings"
	"testing"
)

func TestBlocks(t *testing.T) {
	for _, s := range []string{
		"key is AKIAIOSFODNN7EXAMPLE ok",
		"use ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789AB",
		"ANTHROPIC_API_KEY=sk-ant-" + "api03-abcdefghijklmnopqrstuvwxyz",
		"stripe sk_live_" + "abcdefghijklmnopqrstuvwx",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"DATABASE_URL=postgres://app:hunter2pass@db.example.com/app",
		"STRIPE_SECRET=abc123def456ghi789",
		`"password": "Tr0ub4dor3xyz1"`,
		"openrouter sk-or-v1-" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"hf token hf_" + "abcdefghijklmnopqrstuvwxyzABCDEFGH",
		"gitlab glpat-" + "abcdefghij0123456789",
		"npm npm_" + "abcdefghijklmnopqrstuvwxyz0123456789",
		"publicKeyToken=Tr0ub4dor3xyz1", // the name is .NET's only for its 16 hex digits (#737)
	} {
		if Check(s) == nil {
			t.Errorf("not blocked: %q", s)
		}
	}
}

// TestNames checks that the error names the kind of secret found, most of all where two
// patterns match the same text: an OpenRouter key also fits the OpenAI-style pattern.
func TestNames(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"sk-or-v1-" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "an OpenRouter API key"},
		{"sk-proj-" + "abcdefghijklmnopqrstuvwxyz0123456789", "an OpenAI-style API key"},
		{"hf_" + "abcdefghijklmnopqrstuvwxyzABCDEFGH", "a Hugging Face token"},
		{"glpat-" + "abcdefghij0123456789", "a GitLab personal access token"},
		{"npm_" + "abcdefghijklmnopqrstuvwxyz0123456789", "an npm token"},
	} {
		err := Check(c.text)
		if err == nil {
			t.Errorf("not blocked: %q", c.text)
		} else if !strings.Contains(err.Error(), "looks like "+c.want+".") {
			t.Errorf("%q: error names the wrong kind, want %q: %v", c.text, c.want, err)
		}
	}
}

func TestPasses(t *testing.T) {
	for _, s := range []string{
		"Are you changing the auth middleware?",
		"API_KEY = process.env.API_KEY",
		"set STRIPE_SECRET_KEY in your .env, not here",
		"TOKEN=<your token>",
		"PASSWORD=${DB_PASSWORD}",
		"see https://github.com/a-Fig/murmur/pull/7",
		"the sk- prefix is for keys",
		"hf_",
		"hf_short",
		"glpat-x",
		"run npm_install before the tests",
		`<assemblyIdentity name="Microsoft.Windows.Common-Controls" publicKeyToken="6595b64144ccf1df"/>`, // every Windows program's manifest (#737)
		"npm_" + "abcdefghijklmnopqrstuvwxyz012345",                                                      // 32 characters: too short for a token
		"sk-or-v1-abc",
	} {
		if err := Check(s); err != nil {
			t.Errorf("blocked %q: %v", s, err)
		}
	}
}
