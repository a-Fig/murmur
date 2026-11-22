package scrub

import (
	"strings"
	"testing"
)

// A password's quoted value is judged whole, so a passphrase whose first word is all letters is
// blocked, not let through on that word (#948).
func TestBlocksPassphrases(t *testing.T) {
	for _, s := range []string{
		`{"user": "bob", "password": "purple monkey 42 dishwasher"}`,
		`"db_password": "correct horse battery 9staple"`,
		`password: 'blue Sky 7 rocket launch'`,
		`"passphrase": "tango 88 whiskey foxtrot"`,
		`SMTP_PASSWD="Tr0ub4dor 3xyz"`,
		`{"password":"my dog Rex 2019!"}`,
		`"password": "abc123def456ghi7 later"`, // a first word that looks secret, as before
	} {
		err := Check(s)
		if err == nil {
			t.Errorf("not blocked: %q", s)
		} else if !strings.Contains(err.Error(), "looks like a credential assignment.") {
			t.Errorf("%q: %v", s, err)
		}
	}
}

// What a form or its docs say about a password, under a password's name, passes, and so does a
// value under another name, whose first word alone is judged, as before.
func TestPassesPasswordProse(t *testing.T) {
	for _, s := range []string{
		`"password": "Must be at least 12 characters"`,
		`"passwordHint": "Use 8 or more characters with 1 digit"`,
		`"passwordPolicy": "12 chars min, 1 upper"`,
		`"password_error": "Invalid password, try again 3 times"`,
		`"reset_password_subject": "Reset 2FA on your account"`,
		`"password_label": "Enter your password"`,
		`"password": "Pa55word123 is not allowed"`,
		`"password": "correct horse battery staple"`, // no digit: as PASSWORD=correcthorsebatterystaple
		`"password": "<your password 1>"`,
		`"password": "${DB_PASSWORD} 2"`,
		`"password": "%DB_PASS% 2 backup"`,
		`"password": "(set in step 2 below)"`,
		`"password": "********"`,
		`"password": ""`,
		`"token": "see step 2 and 3 of setup"`,
		`"api_key": "from vault path kv/app2"`,
	} {
		if err := Check(s); err != nil {
			t.Errorf("blocked %q: %v", s, err)
		}
	}
}
