package bus

import "testing"

// The census (site/census/census.mjs, authorPrint) must make the same hash from a commit's author
// email, so these values are in its tests too (site/test/census.test.mjs).
func TestAuthorPrint(t *testing.T) {
	t.Parallel()
	const want = "453bba940d78ae7d" // sha256("murmur-owner\x00github.com/a-fig/murmur\x00ada@example.com")[:16]
	for _, ident := range []string{
		"Ada Lovelace <ada@example.com> 1700000000 +0000",
		"Ada <Ada@Example.COM> 1700000000 -0800",
		"A <b> <ada@example.com> 1700000000 +0000", // a < in the name
		"Ada < ada@example.com > 1700000000 +0000",
	} {
		if got := AuthorPrint("github.com/a-fig/murmur", ident); got != want {
			t.Errorf("AuthorPrint(%q) = %q, want %q", ident, got, want)
		}
	}
	if got := AuthorPrint(" GitHub.com/A-Fig/Murmur ", "Ada <ada@example.com> 0 +0000"); got != want {
		t.Errorf("the repo isn't lowercased and trimmed: %q", got)
	}
	if AuthorPrint("github.com/a-fig/other", "Ada <ada@example.com> 0 +0000") == want {
		t.Error("two repos give one fingerprint for the same email")
	}
	for _, c := range [][2]string{
		{"", "Ada <ada@example.com> 0 +0000"},
		{"github.com/a-fig/murmur", ""},
		{"github.com/a-fig/murmur", "Ada <> 0 +0000"},
		{"github.com/a-fig/murmur", "Ada ada@example.com 0 +0000"},
		{"github.com/a-fig/murmur", "Ada <ada@example.com 0 +0000"},
	} {
		if got := AuthorPrint(c[0], c[1]); got != "" {
			t.Errorf("AuthorPrint(%q, %q) = %q, want none", c[0], c[1], got)
		}
	}
}
