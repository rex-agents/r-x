import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

// SITE and BASE come from actions/configure-pages in CI; the defaults are the project site.
export default defineConfig({
  site: process.env.SITE ?? 'https://rex-agents.github.io',
  base: process.env.BASE ?? '/r-x',
  integrations: [
    starlight({
      title: 'r-x',
      description: 'Isolated agent VMs, their credentials, and a multi-agent harness: rox, rix and rux.',
      social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/rex-agents/r-x' }],
      editLink: { baseUrl: 'https://github.com/rex-agents/r-x/edit/main/' },
      customCss: ['./src/styles/custom.css'],
      // Order inside a group comes from `sidebar.order` in each page's frontmatter.
      sidebar: [
        {
          label: 'Getting started',
          items: [
            { label: 'Installation', slug: 'getting-started/installation' },
            { label: 'Quick start', slug: 'getting-started/quick-start' },
          ],
        },
        { label: 'Using rux', items: [{ autogenerate: { directory: 'rux' } }] },
        { label: 'Managing credentials with rix', items: [{ autogenerate: { directory: 'rix' } }] },
        { label: 'Managing VMs with rox', items: [{ autogenerate: { directory: 'rox' } }] },
      ],
    }),
  ],
});
