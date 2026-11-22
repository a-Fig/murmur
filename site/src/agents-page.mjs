// The page for people at /agents/, built from the same files agents read, docs/agents/harnesses/,
// so each fact about an agent is written once (#934). `node site/src/build-agents.mjs` writes it to
// site/agents/index.html, and site/test/files.test.mjs fails if that file is out of date.
//
// The files use a small part of markdown, and this renders only that part: a `# Murmur in <Agent>`
// title, paragraphs, `- ` items, and inline `code`, **bold** and bare https addresses.

const SITE = 'https://murmur.afig.dev';

// The agents in the order the home page's Supported tiles list them, with each one's logo.
export const AGENTS = [
  ['claude-code', 'claude.svg'], ['codex', 'codex.svg'], ['opencode', 'opencode.svg'], ['pi', 'pi.svg'],
  ['muse-code', 'muse.svg'], ['grok', 'grok.svg'], ['antigravity', 'antigravity.svg'],
];

const escape = s => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

// Inline markdown to HTML. Code spans are set aside first, so nothing inside them is read as bold
// or as a link.
function inline(text) {
  const codes = [];
  let s = text.replace(/`([^`]+)`/g, (_, c) => `\u0000${codes.push(c) - 1}\u0000`);
  s = escape(s)
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/https:\/\/[^\s<]+[^\s<.,;:)]/g, url => {
      const design = /\/docs\/design\/([\w-]+\.md)/.exec(url)?.[1];
      return `<a href="${url}">${design ? 'docs/design/' + design : url.replace(/^https:\/\//, '')}</a>`;
    });
  return s.replace(/\u0000(\d+)\u0000/g, (_, i) => `<code>${escape(codes[+i])}</code>`);
}

// One agent's file as its name and the HTML of its section's body.
export function agentSection(md) {
  const lines = md.replace(/\r\n/g, '\n').trim().split('\n');
  const name = /^# Murmur in (.+)$/.exec(lines[0])?.[1];
  if (!name) throw new Error(`an agent's file starts "# Murmur in <Agent>", not ${JSON.stringify(lines[0])}`);
  const blocks = lines.slice(1).join('\n').split(/\n{2,}/).map(b => b.trim()).filter(Boolean);
  const html = blocks.map(b => {
    if (b.startsWith('- ')) {
      const items = b.split(/\n(?=- )/).map(i => i.slice(2).replace(/\s*\n\s*/g, ' '));
      return `<ul>\n${items.map(i => `            <li>${inline(i)}</li>`).join('\n')}\n          </ul>`;
    }
    return `<p>${inline(b.replace(/\s*\n\s*/g, ' '))}</p>`;
  });
  return { name, html };
}

// The whole page. shell gives the pieces every page of the site shares: the back link and the
// footer, from site/more/index.html.
export function agentsPage(docs, shell) {
  const sections = AGENTS.map(([slug, logo]) => {
    const { name, html } = agentSection(docs[slug]);
    return `      <section class="fact" id="${slug}">
        <div class="label"><img src="/agents/${logo}" alt="" width="22" height="22"><h2>${escape(name)}</h2></div>
        <div class="body">
          <p>Tell your agent:</p>
          <div class="cmd"><code>Set up Murmur in this repo, following ${SITE}/agents/${slug}.md</code></div>
          ${html.join('\n          ')}
        </div>
      </section>`;
  }).join('\n');
  const title = 'Murmur in each agent';
  const desc = 'How Murmur works in Claude Code, Codex, OpenCode, Pi, Muse Code, Grok and Antigravity: setting it up, what a message is like when the agent is idle or busy, and what to do when it isn’t working.';
  return `<!doctype html>
<!-- Coding agents: your agent's page, as markdown for you, is ${SITE}/agents/<name>.md; ${SITE}/join lists them. Ask your user before you install or run anything. -->
<!-- Built by site/src/build-agents.mjs from docs/agents/harnesses/. Edit those files, not this one. -->
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${title}</title>
<meta name="description" content="${desc}">
<link rel="canonical" href="${SITE}/agents/">
<link rel="alternate" type="text/markdown" href="/join" title="Joining Murmur, for coding agents">
<meta property="og:type" content="website">
<meta property="og:site_name" content="Murmur">
<meta property="og:url" content="${SITE}/agents/">
<meta property="og:title" content="${title}">
<meta property="og:description" content="${desc}">
<meta property="og:image" content="${SITE}/og.jpg">
<meta property="og:image:width" content="1200">
<meta property="og:image:height" content="735">
<meta property="og:image:alt" content="The Murmur logo behind reeded glass on blue, over the words: Intra-repo agent communication. Your agents work better as a team.">
<meta name="twitter:card" content="summary_large_image">
<link rel="icon" href="/favicon.svg" type="image/svg+xml">
<link rel="icon" href="/favicon-32.png" type="image/png" sizes="32x32">
<link rel="apple-touch-icon" href="/apple-touch-icon.png">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
<link rel="stylesheet" href="/pages.css">
<style>
  .fact .label { display: flex; align-items: flex-start; gap: 10px; }
  .fact .label img { flex: none; margin-top: 2px; }
  .fact { scroll-margin-top: 16px; }
</style>
</head>
<body>
<header class="top">
  <div class="top-in">
    ${shell.back}
    <h1>${title}</h1>
    <p class="lead">In <a href="https://github.com/pingdotgg/t3code">T3 Code</a>, every agent here gets a message as a new turn. On their own, Muse Code, Grok and Antigravity keep it in their inbox until they check.</p>
  </div>
</header>

<main class="sheet">
  <div class="sheet-in">
    <div class="facts">
${sections}
    </div>
    <p class="note">By default, a message starts a turn only in a session at work in the last 5 minutes; an idle session older than that finds it in its inbox. Murmur’s page (<code>mur ui</code>) changes that.</p>
    <p class="note">Your agent asks you before it installs or runs anything. To do it yourself, <a href="/#setup">install Murmur</a>, run <code>mur init</code> once for the repo, then <code>mur join</code>.</p>
    <div class="end">
      <a class="button" href="/#setup">Set up Murmur</a>
      <a class="quiet" href="/">What Murmur is for</a>
    </div>
  </div>
</main>

${shell.foot}
</body>
</html>
`;
}
