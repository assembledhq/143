# Public Docs Authoring

Fumadocs owns the visible page title and lede for every public docs page. The MDX body should start with useful page content, not a second copy of the page header.

Rules:

- Keep `title` in frontmatter and let the docs layout render it.
- Keep `description` in frontmatter and let the docs layout render it as the visible lede and metadata description.
- Do not start MDX pages with a duplicate `# Title`.
- Make the first body paragraph add context, prerequisites, tradeoffs, or the first action. It should not repeat the frontmatter description.
- Use `##` and deeper headings for body sections only.
- Section overview pages and regular docs pages follow the same rule.
- Raw Markdown routes synthesize `# Title` and the lede from frontmatter, so source MDX should still avoid duplicating them.

This keeps the public docs closer to Stripe-like quality: one clear page header, one concise lede, then body content that immediately moves the reader forward.
