// Writes site/agents/index.html from docs/agents/harnesses/ (src/agents-page.mjs says why).
// Run from anywhere: node site/src/build-agents.mjs
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { AGENTS, agentsPage } from './agents-page.mjs';

const at = path => fileURLToPath(new URL(path, import.meta.url));

// What every page shares, taken from the page on other ways to use Murmur.
export function shell() {
  const more = readFileSync(at('../more/index.html'), 'utf8');
  return {
    back: /<a class="back"[\s\S]*?<\/a>/.exec(more)[0],
    foot: /<footer class="site-foot">[\s\S]*?<\/footer>/.exec(more)[0],
  };
}

export function build() {
  const docs = Object.fromEntries(AGENTS.map(([slug]) => [slug, readFileSync(at(`../../docs/agents/harnesses/${slug}.md`), 'utf8')]));
  return agentsPage(docs, shell());
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  writeFileSync(at('../agents/index.html'), build());
  console.log('wrote site/agents/index.html');
}
