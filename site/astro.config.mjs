import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

// SITE and BASE come from actions/configure-pages in CI; the defaults are the project site.
export default defineConfig({
  site: process.env.SITE ?? 'https://rex-agents.github.io',
  base: process.env.BASE ?? '/r-x',
  integrations: [
    starlight({
      title: 'r-x',
      description: 'rox, rix and rux',
      social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/rex-agents/r-x' }],
      editLink: { baseUrl: 'https://github.com/rex-agents/r-x/edit/main/' },
      // Every docs/*.md becomes a page; the sidebar lists them without further config.
      sidebar: [{ label: 'Docs', items: [{ autogenerate: { directory: '.' } }] }],
    }),
  ],
});
