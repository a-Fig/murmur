# Pictures in a pull request

A pull request that changes something a person sees (the `mur ui` page, the site under `site/`,
terminal output) shows it in its body: a screenshot of the before and the after, and a GIF when the
change moves or responds to input (Tyler's rule, 2026-11-08). A reviewer judges a layout from a
picture faster than from a diff.

## Getting a picture into the body

`gh` can't upload images. This repo is public, so `scripts/pr-media.sh` puts them on a `pr-media`
branch and prints Markdown that embeds them by URL:

```sh
scripts/pr-media.sh 247 before.png after.png move.gif   # prints one Markdown image per file
scripts/pr-media.sh --remove 247                        # takes the PR's files off the branch again
```

Paste the printed lines under `## Screenshots` in the body. Things worth knowing:

- **Nothing in your checkout changes.** It uses only plumbing (`hash-object`, a temporary index,
  `commit-tree`, `push <sha>:refs/heads/pr-media`), never a checkout, so it is safe while someone
  edits the same working tree. The branch holds only pictures, none of the repo.
- **The URLs are pinned to a commit**, so a later push never changes a picture. Adding the same
  file again with new content gives new URLs; the old ones keep serving the old file.
- **It is public, and permanent.** Anyone can read the branch, and history keeps removed files, so
  `--remove` stops showing a file, it doesn't erase it. Check a screenshot for secrets first: the
  address `mur ui` prints carries a key, so capture the page, not a browser's address bar. Keep each
  file small (a GIF under a few MB); the script warns above 5 MB.
- Names are letters, digits, `.`, `_` and `-`; the extension is png, jpg, gif or webp.
- If two people push at once, it fetches the new tip and tries again, up to five times.

## Taking the pictures

**A page.** `playwright-core` drives a Chromium; `npx playwright install chromium` fetches one, and
`executablePath` points at one you already have if its version differs from the one `playwright-core`
expects. Take the before from a build of the PR's base commit (`git worktree add ../base <sha>`),
the after from the branch, with the same data and window size so the two compare.

```js
import { chromium } from 'playwright-core';
const browser = await chromium.launch();           // or { executablePath: '/path/to/chrome' }
const page = await browser.newPage({ viewport: { width: 1100, height: 700 }, deviceScaleFactor: 2 });
await page.goto(url);                               // the address `mur ui` prints, or the site's dev server
await page.screenshot({ path: 'after.png' });       // fullPage: true for a long page
await browser.close();
```

**Something that moves.** Record a video from Playwright (`recordVideo` on `browser.newContext`, then
`await context.close()` writes the file), then turn it into a GIF with a palette so the colors hold:

```sh
ffmpeg -i recording.webm -vf "fps=12,scale=800:-1:flags=lanczos,split[a][b];[a]palettegen[p];[b][p]paletteuse" move.gif
```

Trim the recording to the change itself (`-ss 1 -t 4` before `-i`), and lower `fps` or `scale` if the
file is large.

**Terminal output.** Put the text in a `<pre>` on a dark page and screenshot that with the same
code, so before and after match.
