# Brand

The brand dates from 2026-11-10. Its source is Tyler's Figma file, "murmur"; the files here were
cut from that file's SVG export, so the shapes are the designer's and nothing was redrawn.

## The mark

Two agents side by side that read as an M: two agents in one repo, talking. The left one has round
eyes; the right one's are slanted like a quotation mark.

| File | What it is |
| --- | --- |
| `docs/assets/brand/mark.svg` | The mark, filled, ink |
| `docs/assets/brand/mark-outline.svg` | The mark, outlined, ink |
| `docs/assets/brand/wordmark.svg` | "murmur" in Fredoka One, as outlines, ink |
| `docs/assets/brand/lockup.svg` | The mark as the M of "murmur", outlined, ink, for light grounds |
| `docs/assets/brand/lockup-on-blue.svg` | The same, filled: cream mark, butter letters |
| `docs/assets/brand/lockup-stacked-on-blue.svg` | The mark over the wordmark, cream and butter |
| `docs/assets/brand/icon.svg` | The cream mark on a blue tile: the tab icon and app icon |
| `docs/assets/banner.svg` | The README's header |
| `docs/assets/brand/social-preview.png` | 1280x640, for the GitHub repo's social preview, which is set by hand in the repo's settings |

`site/favicon.svg` and `internal/web/static/assets/favicon.svg` are copies of `icon.svg`, because
the site and the binary each serve their own files. `site/favicon-32.png` and
`site/apple-touch-icon.png` are rasters of it; the touch icon has square corners, since iOS rounds
them.

The Figma file's rules: leave one eye-width of clear space on every side, keep the mark at least
24px wide and the inline lockup at least 96px wide, and never redraw or stretch either. Another
color is a change of `fill`, not a new drawing.

## Where the logo appears

- README: the banner at the top, the icon at the foot.
- Tab icons: the site's and the `mur ui` page's. `mur ui` also shows the icon in its sidebar and
  on its sign-in gate.
- Home page: the mark in the hero's top left corner on screens 1000px and wider, the mark before
  "Murmur daemon" in the How it works diagram, and the lockup at the start of the footer.
- About, Privacy, Contact and Other ways to use Murmur: the mark in the back pill, the lockup in
  the footer.
- The daemon page: the mark in the back pill, the lockup in the footer, in cream.
- The stats pages: the mark on the header's blue tile.
- `site/og.jpg`, the card a shared link shows: the lockup in its top left corner.

The hero's big word stays "murmur" in Inter behind the glass; it is the site's look, not the logo.

## Color

Three colors and one neutral: blue and cream carry the page, butter is the one loud accent, ink is
the text. Every other step is a tint or shade of these four, and each is a value the site already
uses. `docs/brand.html` shows them all, with the logo on each ground; open it in a browser.

| Name | Hex | Use |
| --- | --- | --- |
| blue/100 | `#DCE6FF` | Chips behind code on cream |
| blue/500 | `#225BFF` | The brand blue: page grounds, the icon's tile, links on cream |
| blue/700 | `#1640BF` | Text on blue/100, pressed states |
| butter/300 | `#FEF6C4` | A quiet highlight |
| butter/500 | `#FDEE8C` | The accent: the hero word, the primary button, message cards |
| butter/700 | `#E6CC3F` | Butter where it needs an edge on cream |
| cream/100 | `#FFFBEA` | The sheets, text on blue and ink, the mark on blue |
| cream/200 | `#F5F2E3` | Surfaces on cream |
| cream/300 | `#DDD7B8` | Borders and rules |
| ink/500 | `#3A4670` | Secondary text |
| ink/700 | `#16265C` | Surfaces on ink |
| ink/900 | `#0E1B45` | Text, dark grounds, the logo on light grounds |

| Token | Light | Dark |
| --- | --- | --- |
| bg/canvas | cream/100 | ink/900 |
| bg/surface | cream/200 | ink/700 |
| bg/brand | blue/500 | blue/500 |
| bg/highlight | butter/500 | butter/500 |
| text/primary | ink/900 | cream/100 |
| text/secondary | ink/500 | cream/300 |
| text/link | blue/500 | butter/500 |
| logo/ink | ink/900 | cream/100 |
| logo/accent | blue/500 | butter/500 |
| border/default | cream/300 | ink/500 |

Contrast, measured 2026-11-10: cream on blue/500 is 5.1:1 and blue/500 on cream is the same, so
either carries body text. Butter on blue/500 is 4.4:1, which is right for the logo and large text
and just short of the 4.5:1 a sentence needs. Ink on butter is 14.1:1.

The Figma file proposes a different palette: a lighter blue `#258EE3`, cream `#FFFDF4`, butter
`#FFEF8C` and navy `#05224D`. The site was built in it on 2026-11-10 and Tyler preferred the look
it already had, so this palette stayed and the logo files were colored to match. Cream on
`#258EE3` is 3.4:1, below what body text needs.

## Type

The wordmark is Fredoka One and ships as outlines, so nothing loads the font. The site is set in
Inter, and its hero word is "murmur" in Inter behind the reeded glass, not the wordmark. The Figma
file proposes Rubik for text and JetBrains Mono for commands; neither was adopted.

## The mur ui page

The `mur ui` page is drawn as a sibling of T3 Code, and its Dark, Dim and Light themes are T3
Code's tokens (`internal/web/static/assets/style.css`). They take the icon only.

Its fourth theme, Murmur, is the brand: a cream page, ink text, blue for what is picked or
pressed, and butter for a person's own messages. The sidebar is ink, not blue. Cream on blue is
5.1:1, which leaves no room for quieter text, and the sidebar is mostly quieter text; on ink the
quiet step (`#A9B6E6`) is still 8:1. The greens, ambers and reds of session states stay Light's,
a step darker for cream. Dark stays the default.

### The v3 page

A second page, `/assets/v3/page.html` (`internal/web/static/assets/v3/`), builds the Figma file's
"mur ui v3" screens as they are drawn, on the same `/api` and the same `#token=`. It is apart
from the page above, not a theme of it (2026-11-11). It is set in Rubik, takes its colors and
backdrop from the Figma frames, and has Light and Dark.

Each agent there is a cat from `internal/web/static/assets/cats/`: the 54 looks in the Figma
file's "Cat Avatar — gallery" as of 2026-11-11. That is six tones (Butter, Cream, Blue, Navy, Sky,
Ginger; Gold was dropped) and not every face in every tone. A name hashes to one of them, so an
agent keeps its cat everywhere. A change to the gallery means exporting the cats again and
updating the list in `v3/app.js`.

## Renders

`site/how-wide.mp4`, `site/how-tall.mp4` and their posters are rendered from
`site/src/how-scene.html` by `site/src/render-video.mjs`. `site/og.jpg` and
`docs/assets/murmur-demo.gif` come from the wide film: the card is its last frame with the lockup
set in the top left corner, and the GIF is the film at about 14 frames a second, 960px wide. A
change to the palette means rendering all of them again.
