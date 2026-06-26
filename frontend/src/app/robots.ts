import type { MetadataRoute } from "next";

// Crawl policy for 143.dev. The public marketing + docs pages are crawlable by
// any bot; the authenticated app and backend API are disallowed.
//
// This is the *advisory* layer — compliant crawlers read it and self-restrict.
// The hard boundary (a 403 for un-allowlisted bots) is enforced separately at
// the Cloudflare edge. Keep the two in sync: a path that is Disallow-ed here
// should also stay blocked at the edge, and a marketing path opened at the edge
// should remain crawlable here.
export default function robots(): MetadataRoute.Robots {
  return {
    rules: [
      {
        userAgent: "*",
        allow: "/",
        disallow: [
          // Backend API + auth surfaces.
          "/api/",
          "/login",
          "/invite/",
          "/verify-email",
          // Authenticated dashboard.
          "/sessions",
          "/projects",
          "/settings",
          "/team",
          "/agent",
          "/automations",
          "/autopilot",
          "/integrations",
          "/llm",
          "/repositories",
          "/previews",
          "/onboarding",
        ],
      },
    ],
    sitemap: "https://143.dev/sitemap.xml",
  };
}
