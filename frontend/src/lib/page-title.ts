const APP_TITLE = "143";
const TITLE_SEPARATOR = " | ";
const MAX_PAGE_TITLE_LENGTH = 70;

type PageTitleRule = {
  pattern: RegExp;
  title: string;
};

const PAGE_TITLE_RULES: PageTitleRule[] = [
  { pattern: /^\/$/, title: "Home" },
  { pattern: /^\/about$/, title: "About" },
  { pattern: /^\/privacy$/, title: "Privacy" },
  { pattern: /^\/security$/, title: "Security" },
  { pattern: /^\/terms$/, title: "Terms" },
  { pattern: /^\/login$/, title: "Login" },
  { pattern: /^\/invite\/accept$/, title: "Accept invite" },
  { pattern: /^\/onboarding$/, title: "Onboarding" },
  { pattern: /^\/autopilot$/, title: "Autopilot" },
  { pattern: /^\/autopilot\/decisions$/, title: "Autopilot decisions" },
  { pattern: /^\/sessions$/, title: "Sessions" },
  { pattern: /^\/sessions\/new$/, title: "New session" },
  { pattern: /^\/sessions\/[^/]+$/, title: "Session" },
  { pattern: /^\/automations$/, title: "Automations" },
  { pattern: /^\/automations\/new$/, title: "New automation" },
  { pattern: /^\/automations\/templates$/, title: "Automation templates" },
  { pattern: /^\/automations\/[^/]+$/, title: "Automation" },
  { pattern: /^\/projects$/, title: "Projects" },
  { pattern: /^\/projects\/new$/, title: "New project" },
  { pattern: /^\/projects\/[^/]+$/, title: "Project" },
  { pattern: /^\/repositories\/[^/]+$/, title: "Repository" },
  { pattern: /^\/agent$/, title: "Agent" },
  { pattern: /^\/llm$/, title: "LLM" },
  { pattern: /^\/integrations$/, title: "Integrations" },
  { pattern: /^\/team$/, title: "Team" },
  { pattern: /^\/settings$/, title: "Settings" },
  { pattern: /^\/settings\/account$/, title: "Account settings" },
  { pattern: /^\/settings\/agent$/, title: "Agent settings" },
  { pattern: /^\/settings\/audit-log$/, title: "Audit log" },
  { pattern: /^\/settings\/autopilot$/, title: "Autopilot settings" },
  { pattern: /^\/settings\/evals$/, title: "Evals" },
  { pattern: /^\/settings\/evals\/new$/, title: "New eval" },
  { pattern: /^\/settings\/evals\/batch\/[^/]+$/, title: "Eval batch" },
  { pattern: /^\/settings\/evals\/[^/]+$/, title: "Eval" },
  { pattern: /^\/settings\/integrations$/, title: "Integration settings" },
  { pattern: /^\/settings\/integrations\/github\/setup$/, title: "GitHub setup" },
  { pattern: /^\/settings\/llm$/, title: "LLM settings" },
  { pattern: /^\/settings\/runtime$/, title: "Runtime settings" },
  { pattern: /^\/settings\/team$/, title: "Team settings" },
  { pattern: /^\/settings\/usage$/, title: "Usage" },
];

function normalizePathname(pathname: string): string {
  const withoutQuery = pathname.split("?")[0]?.split("#")[0] ?? "/";
  const withSlash = withoutQuery.startsWith("/") ? withoutQuery : `/${withoutQuery}`;
  return withSlash.replace(/\/+$/, "") || "/";
}

function isLikelyOpaqueID(segment: string): boolean {
  return (
    /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i.test(segment) ||
    (/^[a-z]+-[0-9a-z-]+$/i.test(segment) && /\d/.test(segment)) ||
    /^[0-9a-f]{12,}$/i.test(segment)
  );
}

function humanizeSegment(segment: string): string {
  return segment
    .replaceAll("-", " ")
    .replaceAll("_", " ")
    .replace(/\s+/g, " ")
    .trim()
    .replace(/^./, (match) => match.toUpperCase());
}

export function sanitizePageTitle(title: string | null | undefined): string {
  const normalized = (title ?? "").replace(/\s+/g, " ").trim();
  if (normalized.length <= MAX_PAGE_TITLE_LENGTH) {
    return normalized;
  }

  return `${normalized.slice(0, MAX_PAGE_TITLE_LENGTH - 3).trimEnd()}...`;
}

export function buildDocumentTitle(pageTitle: string | null | undefined): string {
  const sanitized = sanitizePageTitle(pageTitle);
  return sanitized ? `${APP_TITLE}${TITLE_SEPARATOR}${sanitized}` : APP_TITLE;
}

export function resolvePageTitle(pathname: string | null | undefined): string {
  const normalized = normalizePathname(pathname ?? "/");
  const explicit = PAGE_TITLE_RULES.find((rule) => rule.pattern.test(normalized));
  if (explicit) {
    return explicit.title;
  }

  const segments = normalized.split("/").filter(Boolean);
  const lastReadableSegment = [...segments].reverse().find((segment) => !isLikelyOpaqueID(segment));
  return lastReadableSegment ? humanizeSegment(lastReadableSegment) : "Home";
}
