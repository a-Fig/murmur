// Renders og.jpg, the card a shared link shows, from the home page's hero. From site/:
//   node src/render-og.mjs index.html og.jpg
// Needs playwright-core importable, Chromium at the path below, and ffmpeg on PATH (as
// render-video.mjs does). All six pages share the card, so a change to the hero means running it
// again, and a change to what it shows means updating each page's og:image:alt.
import { chromium } from 'playwright-core';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import fs from 'node:fs';
import { pathToFileURL } from 'node:url';

const [source = 'index.html', out = 'og.jpg'] = process.argv.slice(2);
const W = 1200, H = 735;
const chrome = path.join(process.env.LOCALAPPDATA, 'ms-playwright', 'chromium-1248', 'chrome-win64', 'chrome.exe');

// The hero alone, at the card's size: the logo, the headline, the sentence and the picture. What a
// visitor acts on (the bar's links, the prompt, the note for agents) is left out, and the type is
// larger, since a card is read at a third of this size.
const CARD = `
  .agent-note, .bar nav, .bar-gh, .bar-cta, .prompt-h, .prompt, .st-after, .st-facts, .sheet { display: none !important; }
  .st { min-height: ${H}px !important; padding-top: 96px !important; padding-bottom: 40px !important; }
  .bar { height: 96px !important; }
  .bar-logo svg { width: 150px; height: 43px; }
  h1 { font-size: 62px !important; }
  .st-what { font-size: 23px !important; max-width: 24em !important; margin-top: 30px !important; }
  .st-demo { --mw: 440px !important; }
`;

const br = await chromium.launch({ executablePath: chrome });
const png = path.join(path.dirname(out), `.og-${process.pid}.png`);
try {
  const page = await br.newPage({ viewport: { width: W, height: H }, deviceScaleFactor: 2 });
  await page.goto(pathToFileURL(path.resolve(source)).href);
  await page.addStyleTag({ content: CARD });
  await page.evaluate(() => document.fonts.ready);
  // Both messages have arrived by 2.4 s.
  await page.waitForTimeout(3300);
  await page.screenshot({ path: png });
  const ff = spawnSync('ffmpeg', ['-y', '-loglevel', 'error', '-i', png, '-vf', `scale=${W}:${H}:flags=lanczos`, '-q:v', '2', out], { stdio: 'inherit' });
  if (ff.status !== 0) throw new Error('ffmpeg failed');
} finally {
  await br.close();
  fs.rmSync(png, { force: true });
}
