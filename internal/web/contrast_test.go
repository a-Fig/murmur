package web

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The hero appearance (#398) takes its colors from the Murmur website's hero (murmur.afig.dev).
// This test keeps that palette honest: every color the theme puts next to another on the page
// holds WCAG 2.2 AA (#382) — 4.5:1 for text, 3:1 for edges, focus rings and state marks. The
// palette is parsed out of the stylesheet the page actually serves, so the test fails if the
// CSS and the checked values drift apart.

// rgba is a color: red, green, blue in 0-255 and an alpha in 0-1.
type rgba [4]float64

var declRe = regexp.MustCompile(`--([a-z0-9-]+):\s*([^;]+);`)
var hexRe = regexp.MustCompile(`^#([0-9A-Fa-f]{6})$`)
var rgbRe = regexp.MustCompile(`^rgb\((\d+) (\d+) (\d+) / (\d+)%\)$`)
var varRe = regexp.MustCompile(`^var\(--([a-z0-9-]+)\)$`)

// heroPalette reads the .hero token block out of the page's stylesheet.
func heroPalette(t *testing.T) map[string]string {
	t.Helper()
	cssB, err := static.ReadFile("static/assets/style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	css := string(cssB)
	i := strings.Index(css, "\n.hero {")
	if i < 0 {
		t.Fatal("style.css has no .hero block")
	}
	block := css[i:]
	if j := strings.Index(block, "\n}"); j >= 0 {
		block = block[:j]
	}
	pal := make(map[string]string)
	for _, d := range declRe.FindAllStringSubmatch(block, -1) {
		pal[d[1]] = strings.TrimSpace(d[2])
	}
	for _, name := range []string{"background", "foreground", "muted-foreground", "card",
		"popover", "message-surface", "border", "input", "ring", "error", "error-foreground",
		"success", "success-foreground", "warning", "warning-foreground", "pr-open",
		"pr-draft", "secondary", "secondary-foreground"} {
		if _, ok := pal[name]; !ok {
			t.Fatalf("the .hero block is missing --%s", name)
		}
	}
	return pal
}

// col resolves a palette value to a color. It knows the three forms the hero block writes:
// #RRGGBB, rgb(R G B / A%) and one var() hop.
func col(t *testing.T, pal map[string]string, name string) rgba {
	t.Helper()
	v := pal[name]
	if m := varRe.FindStringSubmatch(v); m != nil {
		return col(t, pal, m[1])
	}
	if m := hexRe.FindStringSubmatch(v); m != nil {
		n, err := strconv.ParseUint(m[1], 16, 32)
		if err != nil {
			t.Fatalf("--%s: %q: %v", name, v, err)
		}
		return rgba{float64(n >> 16 & 0xFF), float64(n >> 8 & 0xFF), float64(n & 0xFF), 1}
	}
	if m := rgbRe.FindStringSubmatch(v); m != nil {
		r, _ := strconv.Atoi(m[1])
		g, _ := strconv.Atoi(m[2])
		b, _ := strconv.Atoi(m[3])
		a, _ := strconv.Atoi(m[4])
		return rgba{float64(r), float64(g), float64(b), float64(a) / 100}
	}
	t.Fatalf("--%s: unparseable color %q", name, v)
	return rgba{}
}

// over puts fg at alpha over bg, the way the page composites a translucent fill or wash.
func over(fg, bg rgba, alpha float64) rgba {
	var out rgba
	for i := 0; i < 3; i++ {
		out[i] = fg[i]*alpha + bg[i]*(1-alpha)
	}
	out[3] = 1
	return out
}

// mix blends two colors in sRGB, like color-mix(in srgb, A p%, B).
func mix(a, b rgba, p float64) rgba {
	var out rgba
	for i := 0; i < 3; i++ {
		out[i] = a[i]*p + b[i]*(1-p)
	}
	out[3] = 1
	return out
}

func channel(c float64) float64 {
	c /= 255
	if c <= 0.03928 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func luminance(c rgba) float64 {
	return 0.2126*channel(c[0]) + 0.7152*channel(c[1]) + 0.0722*channel(c[2])
}

func contrast(a, b rgba) float64 {
	hi, lo := luminance(a), luminance(b)
	if lo > hi {
		hi, lo = lo, hi
	}
	return (hi + 0.05) / (lo + 0.05)
}

func TestHeroPaletteContrast(t *testing.T) {
	pal := heroPalette(t)
	bg := col(t, pal, "background") // the site's --blue: the page, sidebar and topbar
	fg := col(t, pal, "foreground") // the site's --cream
	muted := col(t, pal, "muted-foreground")
	card := col(t, pal, "card") // the ground darkened with ink: popovers, hovers
	butter := col(t, pal, "message-surface")
	ink := col(t, pal, "border") // = --input: edges, fields, the segmented track

	// The site's own values, verbatim: its four colors, and the muted line as the hero
	// paragraph's mix resolved. The panel is the ground worked 10% toward ink, as the site
	// darkens its cream panels.
	if got, want := pal["background"], "#225BFF"; got != want {
		t.Errorf("--background = %q, want the site's --blue %q", got, want)
	}
	if got, want := pal["foreground"], "#FFFBEA"; got != want {
		t.Errorf("--foreground = %q, want the site's --cream %q", got, want)
	}
	if got, want := pal["message-surface"], "#FDEE8C"; got != want {
		t.Errorf("--message-surface = %q, want the site's --butter %q", got, want)
	}
	if got, want := pal["border"], "#0E1B45"; got != want {
		t.Errorf("--border = %q, want the site's --ink %q", got, want)
	}
	siteMuted := mix(fg, bg, 0.92)
	for i := 0; i < 3; i++ {
		if math.Abs(siteMuted[i]-muted[i]) > 1 {
			t.Errorf("--muted-foreground %v is not the site's 92%% cream into blue (≈ %v)", muted, siteMuted)
			break
		}
	}
	siteCard := mix(bg, ink, 0.90)
	for i := 0; i < 3; i++ {
		if math.Abs(siteCard[i]-card[i]) > 1 {
			t.Errorf("--card %v is not the ground 90%% into ink (≈ %v)", card, siteCard)
			break
		}
	}

	// The surfaces the page composites out of the palette.
	fill := over(card, bg, 0.40)   // a settings group: .group's card fill over the ground
	badge := over(ink, fill, 0.16) // a state badge or the agent badge on their ink wash

	// Text: 4.5:1 everywhere it is drawn, at the sizes the page draws it.
	text := []struct {
		name        string
		fg, surface rgba
	}{
		{"page text on the ground", fg, bg},
		{"page text on a group", fg, fill},
		{"page text on a popover", fg, card},
		{"page text on an ink field or chip", fg, ink},
		{"muted text on the ground", muted, bg},
		{"muted text on a group", muted, fill},
		{"muted text on the segmented track", muted, ink},
		{"muted text in an ink field (placeholder)", muted, ink},
		{"ink text on butter (message bodies, the chosen segment, butter buttons)", ink, butter},
		{"error text on a group", col(t, pal, "error-foreground"), fill},
		{"error text on the ground", col(t, pal, "error-foreground"), bg},
		{"success badge text", col(t, pal, "success-foreground"), badge},
		{"warning badge text", col(t, pal, "warning-foreground"), badge},
		{"agent badge text", col(t, pal, "secondary-foreground"), badge},
		{"open pull-request link on a group", col(t, pal, "pr-open"), fill},
		{"draft pull-request link on a group", col(t, pal, "pr-draft"), fill},
		{"a doing line on a group (90% foreground)", over(fg, fill, 0.90), fill},
		{"a message line on the ground", fg, bg},
	}
	for _, p := range text {
		if r := contrast(p.fg, p.surface); r < 4.5 {
			t.Errorf("%s: %.3f:1, want at least 4.5:1", p.name, r)
		}
	}

	// Edges, focus rings and state marks: 3:1 against what they sit on.
	marks := []struct {
		name          string
		mark, surface rgba
	}{
		{"a group's ink edge against the ground", ink, bg},
		{"a group's ink edge against its fill", ink, fill},
		{"an ink field against the ground", ink, bg},
		{"a butter button against the ground", butter, bg},
		{"the chosen segment against its ink track", butter, ink},
		{"the focus ring on the ground", col(t, pal, "ring"), bg},
		{"the focus ring on an ink field", col(t, pal, "ring"), ink},
		{"the ink focus ring on a butter button", ink, butter},
		{"the reachable dot", col(t, pal, "success"), bg},
		{"the disconnected dot", col(t, pal, "warning"), bg},
		{"the gone-wrong dot", col(t, pal, "error"), bg},
		{"the idle dot (75% muted)", over(muted, bg, 0.75), bg},
	}
	for _, p := range marks {
		if r := contrast(p.mark, p.surface); r < 3 {
			t.Errorf("%s: %.3f:1, want at least 3:1", p.name, r)
		}
	}
}

// TestHeroAppearanceChoice keeps the theme reachable: the row carries the choice, and the
// pre-paint script applies it.
func TestHeroAppearanceChoice(t *testing.T) {
	app, err := static.ReadFile("static/assets/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	if !strings.Contains(string(app), `["hero", "Hero"]`) {
		t.Error("the Appearance row does not offer the hero choice")
	}
	theme, err := static.ReadFile("static/assets/theme.js")
	if err != nil {
		t.Fatalf("read theme.js: %v", err)
	}
	for _, want := range []string{
		`classList.toggle("hero"`,
		`choice === "hero"`,
	} {
		if !strings.Contains(string(theme), want) {
			t.Errorf("theme.js does not apply the hero choice: missing %s", want)
		}
	}
}
