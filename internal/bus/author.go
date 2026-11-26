package bus

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// AuthorPrint is the commit fingerprint a device writes on each name registry entry (#1244): a
// hash of the repo and the email git writes on this clone's commits, so the census can tie the
// clone to the commit identity it matches in the repo's history, and count one person's clones
// once. repo is the remote as github.com/<owner>/<name> (publicrepo.Address); ident is what
// `git var GIT_AUTHOR_IDENT` prints. It is "" when either is missing.
//
// The census (site/census/census.mjs, authorPrint) hashes every commit author email in the repo
// the same way, so the two must stay byte for byte alike: sha256 of "murmur-owner", the repo and
// the email, lowercased and trimmed, joined by NUL bytes, in hex, cut to 16 characters as the
// census's other hashes are. The repo is in the hash so that a fingerprint links nothing across
// repos by itself; only an account or email already public on commits does.
func AuthorPrint(repo, ident string) string {
	email := identEmail(ident)
	repo = strings.ToLower(strings.TrimSpace(repo))
	if repo == "" || email == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("murmur-owner\x00" + repo + "\x00" + email))
	return hex.EncodeToString(sum[:])[:16]
}

// identEmail is the email in a git ident line ("Name <email> 1700000000 +0000"), trimmed and
// lowercased, or "". The email is between the last < and the > after it: a name can hold a <,
// and git keeps the address itself free of both.
func identEmail(ident string) string {
	i := strings.LastIndexByte(ident, '<')
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(ident[i:], '>')
	if j < 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(ident[i+1 : i+j]))
}
