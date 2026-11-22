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
		// Found in review (2026-11-21): sentences, punctuation, a tab,
		// and YAML's doubled quote.
		`"password": "Mary had a little lamb 1987"`,
		`"password": "the purple cow 42 jumps"`,
		`"password": "long key 42 for my door"`,
		`"password": "purple monkey 42 dishwasher;"`,
		"\"password\": \"purple\tmonkey 42 dishwasher\"",
		`password: 'it''s 42 purple monkeys'`,
		// An assignment inside another's quoted value, and one after a stray quote, as before.
		`"secret_note": "use SECRET_KEY=Xk9mPq2LvR7tN4wZ instead"`,
		`"token_help": "run with API_KEY=Xk9mPq2LvR7tN4wZ set"`,
		`"password_hint": "or set DB_PASSWORD=Sup3rS3cretPass! in env"`,
		`Set API_KEY=" in .env, then SECRET_TOKEN="Xk9mPq2LvR7tN4wZ"`,
		`"token": "Xk9mPq2LvR7tN4wZ'x"`,
		// Found in the third review (2026-11-21): one everyday word, a $ for an s, and a config's
		// or a form's real password under a name like a form's.
		`"password": "Max Power 2019 rocket"`,
		`"password": "strong horse 42 battery"`,
		`"password": "set the table 42 now"`,
		`"password": "menos es mas 42 siempre"`,
		`"password": "moins cher 42 toujours"`,
		`"password": "see you at 42 noon"`,
		`"password": "same old 77 story"`,
		`"password": "short stack 9 pancakes"`,
		`"password": "weak tea 3 cups daily"`,
		`"password": "step lively 7 friends"`,
		`"password": "below zero 12 kelvin"`,
		`"password": "Pa$$w0rd 42 my secret"`,
		`"password": "Ca$h money 42 rocket"`,
		`"password": "Blue$ky 42 rocket launch"`,
		`ADMIN_RESET_PASSWORD="purple monkey 42 dishwasher"`,
		`{"change_password": "purple monkey 42 dishwasher"}`,
	} {
		err := Check(s)
		if err == nil {
			t.Errorf("not blocked: %q", s)
		} else if !strings.Contains(err.Error(), "looks like a credential assignment.") {
			t.Errorf("%q: %v", s, err)
		}
	}
}

// What a form or its docs say about a password passes, under a password's own name or one about it,
// and so does a value under another name, whose first word alone is judged, as before.
func TestPassesPasswordProse(t *testing.T) {
	for _, s := range []string{
		`"password": "Must be at least 12 characters"`,
		`"password": "At least 8 characters, 1 number"`,
		`"password": "$DB_PASS 2 backup copy"`,
		`publicKeyToken="6595b64144ccf1df extra"`,
		// Names about a password, found in review (2026-11-21).
		`"passwordExpired": "Expired 30 days ago"`,
		`"password_attempts": "3 attempts left"`,
		`"passwordUpdatedAt": "November 21 2026"`,
		`"password_changed": "Changed on 21 Nov 2026"`,
		`"password_reset_sent": "Email sent 2 minutes ago"`,
		`"passwordStrength": "Strong 4 out 4"`,
		`"password_strength_label_3": "Good, add 2 more"`,
		`password_hash: "bcrypt cost 12"`,
		`"password_rotation": "every 90 days"`,
		`"password_policy": "NIST 800 63B"`,
		`PASSWORD_HASHER: "Argon2 PasswordHasher"`,
		// Found in the second review (2026-11-21): forms, notes in a password's place, variables
		// and translations.
		`"reset_password": "Link expires in 24 hours"`,
		`"change_password": "Changed 3 days ago"`,
		`"forgot_password": "Try again in 30 seconds"`,
		`"one_time_password": "Code sent to +1 555 0100"`,
		`"password": "Too short: min. 8"`,
		`"password": "Weak - add 2 more"`,
		`"password": "Strength: 3/4 strong"`,
		`DB_PASSWORD: "set in step 3 below"`,
		`"password": "redacted, see ticket 4821"`,
		`"password": "stored in Vault at secret/db 2"`,
		`"password": "not set yet, PR 948"`,
		`"password": "same as staging v2"`,
		`ADMIN_PASSWORD: "TODO set in prod v2"`,
		`password: "$db_pass 2 fallback"`,
		`"password": "$1 or $2 from args"`,
		`"password": "Passwort muss mindestens 8 Zeichen lang sein"`,
		`"password": "Debe tener al menos 8 caracteres"`,
		`"password": "Au moins 8 caractères"`,
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
