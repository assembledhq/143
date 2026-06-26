import { headers } from "next/headers";

const FALLBACK_ORIGIN = "https://143.dev";

/**
 * Absolute origin (scheme + host) used to build canonical URLs in robots.txt
 * and sitemap.xml.
 *
 * Self-hosting friendly, in priority order:
 *   1. An explicit `SITE_URL` env var (set this when serving behind a CDN or on
 *      multiple hostnames and you want one canonical origin). Read at runtime,
 *      so it works with the prebuilt standalone image.
 *   2. The incoming request's host. Behind our Caddy reverse proxy the original
 *      `Host` is preserved and `X-Forwarded-*` are set, so the app emits URLs
 *      for whatever domain it is actually served on — no config required.
 *   3. The hosted domain, only if there is no request context.
 */
export async function getSiteOrigin(): Promise<string> {
  const explicit = process.env.SITE_URL?.trim();
  if (explicit) return explicit.replace(/\/+$/, "");

  const headerList = await headers();
  const host =
    headerList.get("x-forwarded-host") ?? headerList.get("host");
  if (host) {
    const proto = headerList.get("x-forwarded-proto") ?? "https";
    return `${proto}://${host}`;
  }

  return FALLBACK_ORIGIN;
}
