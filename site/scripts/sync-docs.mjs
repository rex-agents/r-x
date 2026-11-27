// Copies ../README.md (home page) and ../docs/*.md into src/content/docs, which is generated and
// git-ignored. The Markdown stays plain: Starlight needs a `title`, so it is taken from the first
// "# heading" (which is then dropped, since Starlight renders the title), and links between the
// files are rewritten to site URLs.
import { readFileSync, writeFileSync, readdirSync, mkdirSync, rmSync } from 'node:fs';
import { join, basename } from 'node:path';

const root = new URL('../../', import.meta.url).pathname;
const out = new URL('../src/content/docs/', import.meta.url).pathname;
const base = (process.env.BASE ?? '/r-x').replace(/\/$/, '');

rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });

const pages = [{ from: join(root, 'README.md'), slug: 'index', dir: '' }];
for (const f of readdirSync(join(root, 'docs')).filter((f) => f.endsWith('.md')).sort()) {
  pages.push({ from: join(root, 'docs', f), slug: basename(f, '.md'), dir: 'docs/' });
}

// Where each repo file lives on the site.
const url = (repoPath) =>
  repoPath === 'README.md' ? `${base}/` : `${base}/${repoPath.replace(/^docs\//, '').replace(/\.md$/, '')}/`;

for (const p of pages) {
  let text = readFileSync(p.from, 'utf8');
  let title = p.slug;
  text = text.replace(/^# (.+)\n+/m, (_, t) => ((title = t.trim()), ''));
  // [text](x.md#frag) relative to the file -> site URL; external and other links are untouched.
  text = text.replace(/\]\((?!\w+:|#|\/)([^)#\s]+\.md)(#[^)]*)?\)/g, (m, path, frag = '') => {
    const parts = (p.dir + path).split('/');
    const norm = [];
    for (const s of parts) s === '..' ? norm.pop() : s !== '.' && norm.push(s);
    return `](${url(norm.join('/'))}${frag})`;
  });
  const fm = `---\ntitle: ${JSON.stringify(title)}\neditUrl: https://github.com/rex-agents/r-x/edit/main/${p.from.slice(root.length)}\n---\n\n`;
  writeFileSync(join(out, p.slug + '.md'), fm + text);
}
console.log(`synced ${pages.length} pages`);
