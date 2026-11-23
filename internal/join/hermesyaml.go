package join

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Hermes keeps its settings in config.yaml, which its users edit by hand and comment. Join adds one
// name to one list there, plugins.enabled, so it edits that list as text, as it edits JSON
// (jsonedit), and leaves every other line as it was: a YAML library would rewrite the file and lose
// the comments and the user's layout, and would be a new dependency for one list. It reads only the
// forms Hermes writes and a person would (a block list, a one-line flow list, nothing), and reports
// any other as a problem rather than guess: a top-level key spelled in quotes, a flow mapping, an
// anchor, alias or tag where it reads, tabs in the indentation, or several documents.

// yamlLines is a YAML file split into lines, with the line ending it uses.
type yamlLines struct {
	lines []string
	eol   string
	bom   bool // the text began with a byte-order mark, which is kept
}

// bom is UTF-8's byte-order mark, which an editor on Windows may put first.
var bom = string(rune(0xFEFF))

func splitYAML(text string) yamlLines {
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	y := yamlLines{eol: eol}
	if t, ok := strings.CutPrefix(text, bom); ok {
		text, y.bom = t, true
	}
	if text = strings.TrimSuffix(text, "\n"); text != "" {
		y.lines = strings.Split(text, "\n")
	}
	return y
}

func (y yamlLines) String() string {
	out := strings.Join(y.lines, y.eol)
	if len(y.lines) > 0 {
		out += y.eol
	}
	if y.bom {
		out = bom + out
	}
	return out
}

// errYAMLForm is a config.yaml in a form join doesn't edit.
var errYAMLForm = errors.New("it is written in a form join doesn't edit safely")

func yamlFormErr(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{errYAMLForm}, args...)...)
}

// indentOf is the count of spaces a line starts with, and whether its indentation has a tab.
func indentOf(line string) (int, bool) {
	n := len(line) - len(strings.TrimLeft(line, " \t"))
	return len(line) - len(strings.TrimLeft(line, " ")), strings.Contains(line[:n], "\t")
}

// blankOrComment says a line carries nothing: empty, spaces, or a comment.
func blankOrComment(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "#")
}

// yamlComment takes a trailing comment off a value: a # after a space, outside quotes.
func yamlComment(v string) (value, comment string) {
	quote := byte(0)
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#' && (i == 0 || v[i-1] == ' ' || v[i-1] == '\t'):
			return strings.TrimRight(v[:i], " \t"), v[i:]
		}
	}
	return strings.TrimRight(v, " \t"), ""
}

var (
	yamlKeyLine  = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_.-]*)\s*:(?:\s+(.*))?$`)
	yamlKeyStart = regexp.MustCompile(`^["']?[A-Za-z_][A-Za-z0-9_.-]*["']?\s*:`)
	quotedKeyRe  = regexp.MustCompile(`^["'][^"']*["']\s*:`)
	plainScalar  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:/@+-]*$`)
	nullValueYML = map[string]bool{"": true, "~": true, "null": true, "Null": true, "NULL": true}
)

// scalarYAML reads one list item: a plain word or a simply quoted one. ok is false for anything
// else.
func scalarYAML(v string) (string, bool) {
	switch {
	case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' && !strings.ContainsAny(v[1:len(v)-1], `"\`):
		return v[1 : len(v)-1], true
	case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' && !strings.Contains(v[1:len(v)-1], "'"):
		return v[1 : len(v)-1], true
	case plainScalar.MatchString(v):
		return v, true
	}
	return "", false
}

// yamlKey is a key found in a mapping: its line, its indent, the value after the colon (comment
// taken off) and the comment.
type yamlKey struct {
	line, indent   int
	value, comment string
}

// checkYAML refuses the forms join doesn't edit, anywhere in the file: tabs in an indentation, a
// second document, a top-level key it can't read, plugins twice (YAML loaders keep the last, which
// join would not be editing).
func checkYAML(y yamlLines) error {
	plugins := 0
	for i, l := range y.lines {
		if strings.HasPrefix(l, "plugins:") || strings.HasPrefix(l, "plugins ") {
			if plugins++; plugins > 1 {
				return yamlFormErr("plugins is there twice, the second on line %d", i+1)
			}
		}
		if blankOrComment(l) {
			continue
		}
		if _, tab := indentOf(l); tab {
			return yamlFormErr("line %d is indented with a tab", i+1)
		}
		t := strings.TrimSpace(l)
		if (t == "---" || strings.HasPrefix(t, "--- ")) && i > 0 || t == "..." {
			return yamlFormErr("line %d starts another document", i+1)
		}
		if n, _ := indentOf(l); n == 0 {
			if quotedKeyRe.MatchString(l) || strings.HasPrefix(t, "{") || strings.HasPrefix(t, "? ") || strings.HasPrefix(t, "[") {
				return yamlFormErr("line %d isn't a key join can read", i+1)
			}
		}
	}
	return nil
}

// findKey finds key among the keys at indent ind in lines [from, to). It returns where the key's
// block ends too: the first line after it at indent ind or less. Every line at indent ind must be a
// key it can read: one it can't might be key, spelled otherwise, and adding key beside it would
// give the mapping the key twice.
func findKey(y yamlLines, from, to, ind int, key string) (k yamlKey, end int, found bool, err error) {
	for i := from; i < to; i++ {
		l := y.lines[i]
		if blankOrComment(l) {
			continue
		}
		n, _ := indentOf(l)
		if n != ind {
			continue
		}
		m := yamlKeyLine.FindStringSubmatch(l[n:])
		if m == nil && ind > 0 && strings.HasPrefix(l[n:], "- ") {
			continue // a block list's item at its key's indent
		}
		if m == nil {
			if ind == 0 && !yamlKeyStart.MatchString(l) {
				continue // a top-level line checkYAML let through, as a document's start
			}
			return yamlKey{}, 0, false, yamlFormErr("line %d isn't a key join can read", i+1)
		}
		if m[1] != key {
			continue
		}
		v, c := yamlComment(m[2])
		k = yamlKey{line: i, indent: n, value: v, comment: c}
		end = i + 1
		for ; end < to; end++ {
			if blankOrComment(y.lines[end]) {
				continue
			}
			if m, _ := indentOf(y.lines[end]); m <= ind && !(m == ind && strings.HasPrefix(strings.TrimSpace(y.lines[end]), "- ")) {
				break
			}
		}
		// Blank and comment lines at the block's end belong to what follows.
		for end > i+1 && blankOrComment(y.lines[end-1]) {
			end--
		}
		return k, end, true, nil
	}
	return yamlKey{}, 0, false, nil
}

// childIndent is the indent of the first line in [from, to) that carries something, or def.
func childIndent(y yamlLines, from, to, def int) int {
	for i := from; i < to; i++ {
		if !blankOrComment(y.lines[i]) {
			n, _ := indentOf(y.lines[i])
			return n
		}
	}
	return def
}

// yamlList is a list found under a key: a flow list on the key's line, or block items after it.
type yamlList struct {
	key   yamlKey
	flow  bool
	items []string
	// raw is each item as written, quotes and all, for a flow list written back.
	raw   []string
	lines []int // each block item's line
	// itemIndent is the block items' indent, where there are some.
	itemIndent int
}

// readList reads the list that key k, whose block runs to end, holds.
func readList(y yamlLines, k yamlKey, end int) (yamlList, error) {
	l := yamlList{key: k}
	v := k.value
	if strings.ContainsAny(v, "&*!|>") && !strings.HasPrefix(v, "[") {
		return l, yamlFormErr("line %d isn't a list join can read", k.line+1)
	}
	switch {
	case strings.HasPrefix(v, "["):
		if !strings.HasSuffix(v, "]") {
			return l, yamlFormErr("line %d is a list over several lines", k.line+1)
		}
		l.flow = true
		inner := strings.TrimSpace(v[1 : len(v)-1])
		if inner == "" {
			return l, nil
		}
		for _, part := range strings.Split(inner, ",") {
			s, ok := scalarYAML(strings.TrimSpace(part))
			if !ok {
				return l, yamlFormErr("line %d has a list item join can't read", k.line+1)
			}
			l.items, l.raw = append(l.items, s), append(l.raw, strings.TrimSpace(part))
		}
		return l, nil
	case !nullValueYML[v]:
		return l, yamlFormErr("line %d isn't a list", k.line+1)
	}
	l.itemIndent = -1
	for i := k.line + 1; i < end; i++ {
		line := y.lines[i]
		if blankOrComment(line) {
			continue
		}
		n, _ := indentOf(line)
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "- ") && t != "-" || l.itemIndent >= 0 && n != l.itemIndent {
			return l, yamlFormErr("line %d isn't a list item join can read", i+1)
		}
		l.itemIndent = n
		v, _ := yamlComment(strings.TrimSpace(strings.TrimPrefix(t, "-")))
		s, ok := scalarYAML(v)
		if !ok {
			return l, yamlFormErr("line %d isn't a list item join can read", i+1)
		}
		l.items = append(l.items, s)
		l.lines = append(l.lines, i)
	}
	return l, nil
}

// pluginsList finds plugins.<list> in y: whether plugins is there, and the list if it is.
func pluginsList(y yamlLines, list string) (plugins yamlKey, pluginsEnd int, hasPlugins bool, l yamlList, hasList bool, err error) {
	if err = checkYAML(y); err != nil {
		return
	}
	plugins, pluginsEnd, hasPlugins, err = findKey(y, 0, len(y.lines), 0, "plugins")
	if err != nil || !hasPlugins {
		return
	}
	if !nullValueYML[plugins.value] && plugins.value != "{}" {
		err = yamlFormErr("plugins, on line %d, isn't a block join can read", plugins.line+1)
		return
	}
	ind := childIndent(y, plugins.line+1, pluginsEnd, 2)
	if ind == 0 {
		ind = 2
	}
	k, end, ok, err := findKey(y, plugins.line+1, pluginsEnd, ind, list)
	if err != nil || !ok {
		return
	}
	l, err = readList(y, k, end)
	hasList = err == nil
	return
}

// yamlListHas says plugins.<list> in text has name.
func yamlListHas(text, list, name string) (bool, error) {
	_, _, _, l, ok, err := pluginsList(splitYAML(text), list)
	if err != nil || !ok {
		return false, err
	}
	for _, it := range l.items {
		if it == name {
			return true, nil
		}
	}
	return false, nil
}

// addYAMLPlugin adds name to plugins.enabled in text, making the list, and plugins, where they
// aren't there. changed is false where name is there already.
func addYAMLPlugin(text, name string) (out string, changed bool, err error) {
	y := splitYAML(text)
	plugins, pluginsEnd, hasPlugins, l, hasList, err := pluginsList(y, "enabled")
	if err != nil {
		return text, false, err
	}
	for _, it := range l.items {
		if it == name {
			return text, false, nil
		}
	}
	insert := func(at int, lines ...string) {
		y.lines = append(y.lines[:at], append(lines, y.lines[at:]...)...)
	}
	switch {
	case !hasPlugins:
		if len(y.lines) > 0 && strings.TrimSpace(y.lines[len(y.lines)-1]) != "" {
			y.lines = append(y.lines, "")
		}
		y.lines = append(y.lines, "plugins:", "  enabled:", "    - "+name)
	case !hasList:
		ind := childIndent(y, plugins.line+1, pluginsEnd, 2)
		if ind == 0 {
			ind = 2
		}
		if plugins.value != "" { // {}, or null spelled any way: it becomes the block
			y.lines[plugins.line] = strings.TrimRight("plugins: "+plugins.comment, " ")
		}
		pad := strings.Repeat(" ", ind)
		insert(plugins.line+1, pad+"enabled:", pad+"  - "+name)
	case l.flow:
		items := append(append([]string(nil), l.raw...), name)
		line := y.lines[l.key.line]
		head := line[:strings.Index(line, ":")+1]
		y.lines[l.key.line] = strings.TrimRight(head+" ["+strings.Join(items, ", ")+"] "+l.key.comment, " ")
	case len(l.lines) == 0:
		line := y.lines[l.key.line]
		head := line[:strings.Index(line, ":")+1]
		y.lines[l.key.line] = strings.TrimRight(head+" "+l.key.comment, " ")
		insert(l.key.line+1, strings.Repeat(" ", l.key.indent+2)+"- "+name)
	default:
		insert(l.lines[len(l.lines)-1]+1, strings.Repeat(" ", l.itemIndent)+"- "+name)
	}
	return y.String(), true, nil
}

// removeYAMLPlugin takes name out of plugins.enabled in text. A block list left with no items
// becomes [], as Hermes reads an empty value as none too but a person reading it might not.
func removeYAMLPlugin(text, name string) (out string, changed bool, err error) {
	y := splitYAML(text)
	_, _, _, l, hasList, err := pluginsList(y, "enabled")
	if err != nil || !hasList {
		return text, false, err
	}
	at := -1
	for i, it := range l.items {
		if it == name {
			at = i
			break
		}
	}
	if at < 0 {
		return text, false, nil
	}
	line := y.lines[l.key.line]
	head := line[:strings.Index(line, ":")+1]
	if l.flow {
		items := append(append([]string(nil), l.raw[:at]...), l.raw[at+1:]...)
		y.lines[l.key.line] = strings.TrimRight(head+" ["+strings.Join(items, ", ")+"] "+l.key.comment, " ")
		return y.String(), true, nil
	}
	y.lines = append(y.lines[:l.lines[at]], y.lines[l.lines[at]+1:]...)
	if len(l.items) == 1 {
		y.lines[l.key.line] = strings.TrimRight(head+" [] "+l.key.comment, " ")
	}
	return y.String(), true, nil
}
