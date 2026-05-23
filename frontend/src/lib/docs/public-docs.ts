import fs from "node:fs";
import path from "node:path";

export const DOCS_BASE_PATH = "docs/public";

const repoRoot = path.resolve(process.cwd(), "..");
const publicDocsRoot = path.join(repoRoot, DOCS_BASE_PATH);
const publicDocExtensions = [".mdx", ".md"];

export interface PublicDocEntry {
  title: string;
  description: string;
  section: string;
  order: number;
  status: string;
  audience: string;
  tags: string[];
  llmSummary: string;
  slug: string[];
  url: string;
  rawMarkdownUrl: string;
  filePath: string;
}

export interface RawPublicDoc {
  content: string;
  contentType: "text/markdown; charset=utf-8";
  filePath: string;
}

function toPosixPath(value: string) {
  return value.split(path.sep).join("/");
}

function trimQuotes(value: string) {
  const trimmed = value.trim();
  if (
    (trimmed.startsWith('"') && trimmed.endsWith('"')) ||
    (trimmed.startsWith("'") && trimmed.endsWith("'"))
  ) {
    return trimmed.slice(1, -1);
  }
  return trimmed;
}

function parseScalar(value: string) {
  const trimmed = trimQuotes(value);
  if (/^\d+$/.test(trimmed)) {
    return Number(trimmed);
  }
  if (trimmed === "true") {
    return true;
  }
  if (trimmed === "false") {
    return false;
  }
  return trimmed;
}

function parseFrontmatter(source: string): Record<string, unknown> {
  if (!source.startsWith("---\n")) {
    return {};
  }

  const end = source.indexOf("\n---", 4);
  if (end === -1) {
    return {};
  }

  const frontmatter = source.slice(4, end).split("\n");
  const data: Record<string, unknown> = {};
  let activeListKey: string | null = null;

  for (const line of frontmatter) {
    if (!line.trim()) {
      continue;
    }

    const listMatch = line.match(/^\s+-\s+(.+)$/);
    if (listMatch && activeListKey) {
      const current = data[activeListKey];
      if (Array.isArray(current)) {
        current.push(trimQuotes(listMatch[1]));
      }
      continue;
    }

    const keyMatch = line.match(/^([A-Za-z0-9_-]+):\s*(.*)$/);
    if (!keyMatch) {
      activeListKey = null;
      continue;
    }

    const [, key, rawValue] = keyMatch;
    if (rawValue === "") {
      data[key] = [];
      activeListKey = key;
      continue;
    }

    data[key] = parseScalar(rawValue);
    activeListKey = null;
  }

  return data;
}

function slugFromFile(filePath: string) {
  const relative = toPosixPath(path.relative(publicDocsRoot, filePath));
  const withoutExtension = relative.replace(/\.(mdx|md)$/, "");
  const segments = withoutExtension.split("/");

  if (segments.at(-1) === "index") {
    segments.pop();
  }

  return segments;
}

function urlFromSlug(slug: string[]) {
  if (slug.length === 0) {
    return "/docs";
  }
  return `/docs/${slug.join("/")}`;
}

function walkDocs(dir: string): string[] {
  if (!fs.existsSync(dir)) {
    return [];
  }

  const entries = fs.readdirSync(dir, { withFileTypes: true });
  return entries.flatMap((entry) => {
    const entryPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      return walkDocs(entryPath);
    }
    if (entry.isFile() && publicDocExtensions.includes(path.extname(entry.name))) {
      return [entryPath];
    }
    return [];
  });
}

function toPublicDocEntry(filePath: string): PublicDocEntry | null {
  const content = fs.readFileSync(filePath, "utf8");
  const frontmatter = parseFrontmatter(content);
  const title = frontmatter.title;
  const description = frontmatter.description;
  const llmSummary = frontmatter.llm_summary;

  if (
    typeof title !== "string" ||
    typeof description !== "string" ||
    typeof llmSummary !== "string"
  ) {
    return null;
  }

  const slug = slugFromFile(filePath);
  const url = urlFromSlug(slug);
  const tags = Array.isArray(frontmatter.tags)
    ? frontmatter.tags.filter((tag): tag is string => typeof tag === "string")
    : [];

  return {
    title,
    description,
    section: typeof frontmatter.section === "string" ? frontmatter.section : "Guides",
    order: typeof frontmatter.order === "number" ? frontmatter.order : 9999,
    status: typeof frontmatter.status === "string" ? frontmatter.status : "draft",
    audience: typeof frontmatter.audience === "string" ? frontmatter.audience : "engineer",
    tags,
    llmSummary,
    slug,
    url,
    rawMarkdownUrl: `${url}.md`,
    filePath,
  };
}

export function getAllPublicDocs(): PublicDocEntry[] {
  return walkDocs(publicDocsRoot)
    .map(toPublicDocEntry)
    .filter((entry): entry is PublicDocEntry => entry !== null)
    .sort((a, b) => a.order - b.order || a.url.localeCompare(b.url));
}

function normalizeRawSlug(slug: string[]) {
  const normalized = [...slug];
  const last = normalized.at(-1);

  if (last?.endsWith(".md")) {
    normalized[normalized.length - 1] = last.slice(0, -3);
  }

  return normalized.filter(Boolean);
}

function resolvePublicDocPath(slug: string[]) {
  const normalized = normalizeRawSlug(slug);
  const relativePath = normalized.length === 0 ? "index" : normalized.join("/");

  for (const extension of publicDocExtensions) {
    const candidate = path.resolve(publicDocsRoot, `${relativePath}${extension}`);
    if (!candidate.startsWith(publicDocsRoot + path.sep)) {
      continue;
    }
    if (fs.existsSync(candidate)) {
      return candidate;
    }
  }

  for (const extension of publicDocExtensions) {
    const candidate = path.resolve(publicDocsRoot, relativePath, `index${extension}`);
    if (!candidate.startsWith(publicDocsRoot + path.sep)) {
      continue;
    }
    if (fs.existsSync(candidate)) {
      return candidate;
    }
  }

  return null;
}

export function getRawPublicDocBySlug(slug: string[]): RawPublicDoc {
  const filePath = resolvePublicDocPath(slug);

  if (!filePath) {
    throw new Error(`Public doc not found: ${slug.join("/")}`);
  }

  return {
    content: fs.readFileSync(filePath, "utf8"),
    contentType: "text/markdown; charset=utf-8",
    filePath,
  };
}

export function getPublicDocsLlmsText(origin = "https://143.dev") {
  const docs = getAllPublicDocs();
  const lines = [
    "# 143.dev docs",
    "",
    "Public documentation for 143.dev, an open-source platform for coding-agent sessions, previews, review loops, and self-hosted operation.",
    "",
    "Use the canonical page URL for humans and the raw Markdown URL for automated ingestion.",
    "",
    "## Pages",
    "",
  ];

  for (const doc of docs) {
    lines.push(`- [${doc.title}](${origin}${doc.url}): ${doc.llmSummary}`);
    lines.push(`  Raw Markdown: ${origin}${doc.rawMarkdownUrl}`);
  }

  return `${lines.join("\n")}\n`;
}
