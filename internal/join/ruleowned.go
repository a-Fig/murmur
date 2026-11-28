package join

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/a-Fig/murmur/internal/brand"
)

// An allow rule is a string or an entry in a permissions file the user also writes, and the agent
// rewrites those files itself (Cursor, Kiro, Claude Code, Antigravity), so nothing in the rule says
// who added it. A rule that was there before join is the user's, and uninstall must leave it
// (#1199). So join records each rule it adds, in a small file beside the permissions file, and
// uninstall takes out only a rule that is recorded. A rule an older mur added has no record, and
// stays too, with a note that says how to remove it by hand.

// ruleRecordName is the record's file name, in the folder of the permissions file it concerns.
const ruleRecordName = ".murmur-join.json"

// ruleRecordPath is the record of the rules join added to configFile.
func ruleRecordPath(configFile string) string {
	return filepath.Join(filepath.Dir(configFile), ruleRecordName)
}

// ruleRecord maps a permissions file's name to the rules join added to it.
type ruleRecord struct {
	Added map[string][]string `json:"added"`
}

func readRuleRecord(configFile string) (ruleRecord, error) {
	var rec ruleRecord
	b, err := os.ReadFile(resolveLinks(ruleRecordPath(configFile)))
	if errors.Is(err, os.ErrNotExist) {
		return rec, nil
	}
	if err != nil {
		return rec, err
	}
	if json.Unmarshal(b, &rec) != nil {
		return ruleRecord{}, nil // a record that doesn't parse says nothing: its rules stay as they are
	}
	return rec, nil
}

// joinAddedRule says join recorded adding rule to configFile. A record it can't read says no, which
// leaves the rule where it is.
func joinAddedRule(configFile, rule string) bool {
	rec, err := readRuleRecord(configFile)
	return err == nil && slices.Contains(rec.Added[filepath.Base(configFile)], rule)
}

// recordRule notes that join is adding rule to configFile. It is written before the rule is, so a
// failure here leaves the file as it was.
func recordRule(configFile, rule string) error {
	rec, err := readRuleRecord(configFile)
	if err != nil {
		return err
	}
	name := filepath.Base(configFile)
	if slices.Contains(rec.Added[name], rule) {
		return nil
	}
	if rec.Added == nil {
		rec.Added = map[string][]string{}
	}
	rec.Added[name] = append(rec.Added[name], rule)
	return writeRuleRecord(configFile, rec)
}

// forgetRule takes rule out of the record of configFile, and the record's file once it is empty.
func forgetRule(configFile, rule string) error {
	rec, err := readRuleRecord(configFile)
	if err != nil {
		return err
	}
	name := filepath.Base(configFile)
	if !slices.Contains(rec.Added[name], rule) {
		return nil
	}
	if left := slices.DeleteFunc(slices.Clone(rec.Added[name]), func(r string) bool { return r == rule }); len(left) > 0 {
		rec.Added[name] = left
	} else {
		delete(rec.Added, name)
	}
	if len(rec.Added) == 0 {
		if err := os.Remove(ruleRecordPath(configFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeRuleRecord(configFile, rec)
}

func writeRuleRecord(configFile string, rec ruleRecord) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(ruleRecordPath(configFile), append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("%s: %w", ruleRecordPath(configFile), err)
	}
	return nil
}

// takeOutAddedRule runs remove over the permissions file at path, which takes rule out of its text
// and says whether it was there. The file is written only where join recorded adding rule; one
// that is there without a record is the user's, and comes back as a note. A record of a rule that
// is gone is dropped.
func takeOutAddedRule(label, path, rule string, remove func(text string) (string, bool, error)) (removed bool, note string, err error) {
	return takeOutAddedRuleMode(label, path, rule, 0o644, remove)
}

// keptRuleNote says uninstall left rule in path because join didn't record adding it.
func keptRuleNote(label, path, rule string) string {
	return label + ": left the rule " + rule + " in " + path + " alone: " + brand.Command +
		" didn't record adding it, so it is yours (or an older mur's); delete it by hand if you don't want it"
}

// takeOutAddedRuleMode is takeOutAddedRule writing the file with mode perm.
func takeOutAddedRuleMode(label, path, rule string, perm os.FileMode, remove func(text string) (string, bool, error)) (removed bool, note string, err error) {
	text, err := readConfig(path)
	if err != nil {
		return false, "", err
	}
	out, found := text, false
	if text != "" {
		if out, found, err = remove(text); err != nil {
			return false, "", fmt.Errorf("%s: %w", path, err)
		}
	}
	switch {
	case !found:
		return false, "", forgetRule(path, rule)
	case !joinAddedRule(path, rule):
		return false, keptRuleNote(label, path, rule), nil
	}
	if err := writeJSONConfig(path, out, perm); err != nil {
		return false, "", err
	}
	return true, "", forgetRule(path, rule)
}
