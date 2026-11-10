package web

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The Murmur theme is the website's hero (#398). Its colors are written as hex and rgb() so that
// this test can measure them: the page's other themes were measured in a browser (#383), and this
// holds the new one to the same WCAG 2.2 AA bar (1.4.3 text 4.5:1, 1.4.11 edges and dots 3:1).

type rgb struct{ r, g, b float64 }

func (c rgb) lum() float64 {
	f := func(v float64) float64 {
		v /= 255
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*f(c.r) + 0.7152*f(c.g) + 0.0722*f(c.b)
}

func contrast(a, b rgb) float64 {
	x, y := a.lum(), b.lum()
	if x < y {
		x, y = y, x
	}
	return (x + 0.05) / (y + 0.05)
}

// over is fg at alpha over bg.
func over(fg rgb, alpha float64, bg rgb) rgb {
	return rgb{fg.r*alpha + bg.r*(1-alpha), fg.g*alpha + bg.g*(1-alpha), fg.b*alpha + bg.b*(1-alpha)}
}

var (
	hexRe  = regexp.MustCompile(`^#([0-9a-f]{6})$`)
	rgbaRe = regexp.MustCompile(`^rgb\((\d+) (\d+) (\d+)(?: / (\d+)%)?\)$`)
	varRe  = regexp.MustCompile(`^var\((--[a-z-]+)\)$`)
	declRe = regexp.MustCompile(`(--[a-z-]+):\s*([^;]+);`)
)

// cssBlock returns the declarations in the rule that starts with selector.
func cssBlock(t *testing.T, css, selector string) map[string]string {
	t.Helper()
	i := strings.Index(css, selector+" {")
	if i < 0 {
		t.Fatalf("style.css has no %q rule", selector)
	}
	rest := css[i+len(selector)+2:]
	body := rest[:strings.Index(rest, "\n}")]
	body = regexp.MustCompile(`/\*.*?\*/`).ReplaceAllString(body, "")
	out := map[string]string{}
	for _, m := range declRe.FindAllStringSubmatch(body, -1) {
		out[m[1]] = strings.TrimSpace(m[2])
	}
	return out
}

// color resolves a token to a color and an alpha (1 when opaque).
func color(t *testing.T, tokens map[string]string, name string) (rgb, float64) {
	t.Helper()
	v, ok := tokens[name]
	if !ok {
		t.Fatalf("the Murmur theme does not set %s", name)
	}
	if m := varRe.FindStringSubmatch(v); m != nil {
		return color(t, tokens, m[1])
	}
	if m := hexRe.FindStringSubmatch(v); m != nil {
		n, _ := strconv.ParseUint(m[1], 16, 32)
		return rgb{float64(n >> 16 & 255), float64(n >> 8 & 255), float64(n & 255)}, 1
	}
	if m := rgbaRe.FindStringSubmatch(v); m != nil {
		f := func(s string) float64 { x, _ := strconv.ParseFloat(s, 64); return x }
		a := 1.0
		if m[4] != "" {
			a = f(m[4]) / 100
		}
		return rgb{f(m[1]), f(m[2]), f(m[3])}, a
	}
	t.Fatalf("%s: %q is neither hex nor rgb(), so the test cannot measure it", name, v)
	return rgb{}, 0
}

func TestMurmurThemeContrast(t *testing.T) {
	b, err := os.ReadFile("static/assets/style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	root := cssBlock(t, css, `:root[data-theme="murmur"]`)
	side := cssBlock(t, css, `[data-theme="murmur"] [data-app-sidebar]`)

	solid := func(tokens map[string]string, name string) rgb {
		c, a := color(t, tokens, name)
		if a != 1 {
			t.Fatalf("%s is translucent", name)
		}
		return c
	}
	// A surface, and what a row's overlay makes of it.
	type surface struct {
		name string
		bg   rgb
	}
	page, card, pop := solid(root, "--background"), solid(root, "--card"), solid(root, "--popover")
	sidebar := solid(side, "--sidebar")
	tint := func(tokens map[string]string, name string, bg rgb) rgb {
		c, a := color(t, tokens, name)
		return over(c, a, bg)
	}
	surfaces := []surface{
		{"page", page},
		{"card", card},
		{"popover", pop},
		{"message surface", solid(root, "--message-surface")},
		{"composer", solid(root, "--composer")},
		{"page, row hovered", tint(root, "--row-hover", page)},
		{"page, row picked", tint(root, "--row-selected", page)},
		{"page, secondary fill", tint(root, "--accent", page)},
		{"sidebar", sidebar},
		{"sidebar, row hovered", tint(side, "--sidebar-row-hover", sidebar)},
		{"sidebar, row picked", tint(side, "--sidebar-row-active", sidebar)},
	}
	// Text on the page's surfaces, 4.5:1. The sidebar's text tokens are checked on the sidebar.
	pageText := []string{"--foreground", "--muted-foreground", "--secondary-label", "--placeholder",
		"--destructive-foreground", "--info-foreground", "--success-foreground", "--warning-foreground",
		"--pr-open", "--pr-draft", "--primary-text"}
	sideText := []string{"--sidebar-foreground", "--sidebar-muted-foreground"}
	for _, s := range surfaces {
		names := pageText
		tokens := root
		if strings.HasPrefix(s.name, "sidebar") {
			names = append(append([]string{}, sideText...), "--secondary-label")
			tokens = side
			if s.name != "sidebar" {
				// the page's status colors also sit on the sidebar's rows
				names = append(names, pageText[4:]...)
			}
		}
		for _, n := range names {
			tk := tokens
			if _, ok := tk[n]; !ok {
				tk = root
			}
			fg, a := color(t, tk, n)
			if a != 1 {
				t.Fatalf("%s is translucent", n)
			}
			if c := contrast(fg, s.bg); c < 4.5 {
				t.Errorf("%s on %s is %.2f:1, under 4.5:1 (WCAG 1.4.3)", n, s.name, c)
			}
		}
	}
	// The primary button: ink on butter.
	if c := contrast(solid(root, "--primary-foreground"), solid(root, "--primary")); c < 4.5 {
		t.Errorf("--primary-foreground on --primary is %.2f:1, under 4.5:1", c)
	}
	// Edges, rings and the dots that carry state: 3:1 on the surfaces they sit on (1.4.11).
	for _, s := range []surface{{"page", page}, {"card", card}, {"sidebar", sidebar}} {
		for _, n := range []string{"--edge", "--dot-off", "--ring", "--success", "--warning", "--info", "--destructive"} {
			fg, _ := color(t, root, n)
			if c := contrast(fg, s.bg); c < 3 {
				t.Errorf("%s on %s is %.2f:1, under 3:1 (WCAG 1.4.11)", n, s.name, c)
			}
		}
	}
	// The theme is the hero's: its page and text are the site's blue and cream.
	if root["--background"] != "#225bff" || root["--foreground"] != "#fffbea" || root["--primary"] != "#fdee8c" {
		t.Errorf("the theme's blue, cream and butter must stay the website hero's (#225BFF, #FFFBEA, #FDEE8C), got %s %s %s",
			root["--background"], root["--foreground"], root["--primary"])
	}
}

func TestMurmurThemeIsOffered(t *testing.T) {
	b, err := os.ReadFile("static/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), fmt.Sprintf("[%q, %q],", "murmur", "Murmur")) {
		t.Error(`app.js THEMES has no ["murmur", "Murmur"] choice`)
	}
}
