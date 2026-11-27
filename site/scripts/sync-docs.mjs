// Builds src/content/docs (generated, git-ignored) from the repo's Markdown:
//   site/src/landing/index.{md,mdx}  -> /                 (the home page; README.md stays the repo README)
//   docs/**/*.{md,mdx}               -> same path, index.md -> its directory (docs/rux/agents.md -> /rux/agents/)
// A source file may start with YAML frontmatter; it is kept, and `title` is added from the first
// "# heading" (which is dropped, since Starlight renders the title) unless the frontmatter has one.
// `editUrl` is added too. Relative .md/.mdx links are rewritten to site URLs.
import { readFileSync, writeFileSync, readdirSync, mkdirSync, rmSync, existsSync } from 'node:fs';
import { join, dirname, posix } from 'node:path';

const root = new URL('../../', import.meta.url).pathname;
const siteDir = new URL('../', import.meta.url).pathname;
const out = join(siteDir, 'src/content/docs');
const base = (process.env.BASE ?? '/r-x').replace(/\/$/, '');
const isDoc = (f) => /\.mdx?$/.test(f);

rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });

// Every source: `repo` is its repo-relative path, `dest` its path under src/content/docs.
const pages = [];
const landing = ['index.mdx', 'index.md'].map((f) => join(siteDir, 'src/landing', f)).find(existsSync);
if (landing) pages.push({ from: landing, repo: landing.slice(root.length), dest: 'index' + landing.slice(landing.lastIndexOf('.')) });
else console.warn('no site/src/landing/index.mdx; the home page will be missing');

const walk = (dir, rel) => {
  if (!existsSync(dir)) return;
  for (const e of readdirSync(dir, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
    if (e.isDirectory()) walk(join(dir, e.name), posix.join(rel, e.name));
    else if (isDoc(e.name)) pages.push({ from: join(dir, e.name), repo: posix.join('docs', rel, e.name), dest: posix.join(rel, e.name) });
  }
};
walk(join(root, 'docs'), '');

// Site URL for a repo path.
const url = (repoPath) => {
  let p = repoPath.replace(/\.mdx?$/, '');
  if (p === 'site/src/landing/index') return `${base}/`;
  p = p.replace(/^docs\//, '').replace(/(^|\/)index$/, '');
  return `${base}/${p ? p + '/' : ''}`;
};

const unquote = (s) => s.trim().replace(/^(["'])(.*)\1$/, '$2');

// While old flat files (docs/rux.md) and new section dirs (docs/rux/index.md) coexist, the dir wins.
const seen = new Map();
for (const p of pages) {
  const k = url(p.repo);
  const prev = seen.get(k);
  if (!prev || /(^|\/)index\.mdx?$/.test(p.repo)) seen.set(k, p);
}
for (const p of [...seen.values()]) {
  let text = readFileSync(p.from, 'utf8').replace(/\r\n/g, '\n');
  let fm = '';
  const m = text.match(/^---\n([\s\S]*?\n)?---\n/);
  if (m) {
    fm = m[1] ?? '';
    text = text.slice(m[0].length);
  }
  if (!/^title:/m.test(fm)) {
    let title = null;
    text = text.replace(/^[ \t]*\n*# (.+)\n+/, (_, t) => ((title = unquote(t)), ''));
    if (title === null) {
      const h = text.match(/^# (.+)\n+/m); // an H1 after some leading text
      if (h) { title = unquote(h[1]); text = text.replace(h[0], ''); }
    }
    fm = `title: ${JSON.stringify(title ?? p.dest.replace(/\.mdx?$/, ''))}\n` + fm;
  }
  if (!/^editUrl:/m.test(fm)) fm += `editUrl: https://github.com/rex-agents/r-x/edit/main/${p.repo}\n`;

  // [text](x.md#frag) relative to the file -> site URL; external, absolute and #-only links are untouched.
  text = text.replace(/\]\((?!\w+:|#|\/)([^)#\s]+\.mdx?)(#[^)]*)?\)/g, (_, path, frag = '') =>
    `](${url(posix.normalize(posix.join(posix.dirname(p.repo), path)))}${frag})`);

  text = text.replaceAll('__BASE__', base);
  const file = join(out, p.dest);
  mkdirSync(dirname(file), { recursive: true });
  writeFileSync(file, `---\n${fm.replaceAll('__BASE__', base)}---\n\n${text}`);
}
console.log(`synced ${seen.size} pages`);
