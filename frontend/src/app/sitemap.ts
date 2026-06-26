import type { MetadataRoute } from "next";
import { source } from "@/lib/source";

// Absolute origin for sitemap URLs. The hosted service runs on 143.dev; this is
// the same canonical domain hardcoded elsewhere in the app.
const SITE_URL = "https://143.dev";

// Public, linked marketing pages. Intentionally excludes:
//   - auth-gated app + API routes (kept out of crawl scope; see robots.ts)
//   - unlinked / in-progress landing pages (e.g. /why-143) that are not yet
//     meant to be discoverable.
// The /docs surface is not listed here — it comes from the docs source below,
// whose pages already include the /docs index.
const MARKETING_PATHS = ["/", "/about", "/privacy", "/security", "/terms"];

export default function sitemap(): MetadataRoute.Sitemap {
  const marketing: MetadataRoute.Sitemap = MARKETING_PATHS.map((path) => ({
    url: `${SITE_URL}${path}`,
    changeFrequency: "monthly",
    priority: path === "/" ? 1 : 0.7,
  }));

  // Docs pages are generated from the same fumadocs source as /llms.txt, so the
  // sitemap stays in sync as docs are added or removed. page.url already
  // includes the /docs base path (e.g. "/docs", "/docs/getting-started/...").
  const docs: MetadataRoute.Sitemap = source.getPages().map((page) => ({
    url: `${SITE_URL}${page.url}`,
    changeFrequency: "weekly",
    priority: page.url === "/docs" ? 0.8 : 0.6,
  }));

  return [...marketing, ...docs];
}
