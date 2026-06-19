import { existsSync, readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import getStartedMeta from "../../../../docs/public/getting-started/meta.json";
import guidesMeta from "../../../../docs/public/guides/meta.json";
import rootDocsMeta from "../../../../docs/public/meta.json";
import referenceMeta from "../../../../docs/public/reference/meta.json";
import selfHostingMeta from "../../../../docs/public/self-hosting/meta.json";
import {
  DOCS_BASE_PATH,
  getAllPublicDocs,
  getRawPublicDocBySlug,
  getPublicDocsLlmsText,
  type LlmsPage,
} from "./public-docs";

const publicDocsPath = join(process.cwd(), "..", "docs", "public");

function publicMdxFiles(dir = publicDocsPath): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const entryPath = join(dir, entry.name);

    if (entry.isDirectory()) {
      return publicMdxFiles(entryPath);
    }

    return entry.isFile() && entry.name.endsWith(".mdx") ? [entryPath] : [];
  });
}

function readPublicMdx(filePath: string) {
  const content = readFileSync(filePath, "utf8");
  const match = content.match(/^---\n([\s\S]*?)\n---\n([\s\S]*)$/);

  if (!match) {
    throw new Error(`${filePath} is missing frontmatter`);
  }

  const title = match[1].match(/^title:\s*(.+)$/m)?.[1].replace(/^"|"$/g, "");
  const description = match[1]
    .match(/^description:\s*(.+)$/m)?.[1]
    .replace(/^"|"$/g, "");
  const status = match[1].match(/^status:\s*(.+)$/m)?.[1].replace(/^"|"$/g, "");

  if (!title || !description || !status) {
    throw new Error(`${filePath} is missing title, description, or status`);
  }

  return {
    body: match[2].trimStart(),
    description,
    status,
    title,
  };
}

describe("public docs source", () => {
  it("uses docs/public as the only public content root", () => {
    expect(DOCS_BASE_PATH).toBe("docs/public");
  });

  it("does not duplicate section labels in the root docs sidebar", () => {
    const pages = rootDocsMeta.pages;

    expect(pages).not.toContain("---Get started---");
    expect(pages).not.toContain("---Guides---");
    expect(pages).not.toContain("---Self-hosting---");
    expect(pages).not.toContain("---Reference---");
  });

  it("keeps section pages in one navigable sidebar tree", () => {
    const sectionMetas = [
      getStartedMeta,
      guidesMeta,
      selfHostingMeta,
      referenceMeta,
    ];

    expect(sectionMetas.every((meta) => !("root" in meta))).toBe(true);
  });

  it("lets the docs layout own page titles instead of repeating them in MDX", () => {
    for (const filePath of publicMdxFiles()) {
      const page = readPublicMdx(filePath);

      expect(page.body).not.toMatch(new RegExp(`^#\\s+${page.title}\\s*$`, "m"));
    }
  });

  it("uses body introductions for content that goes beyond the metadata description", () => {
    for (const filePath of publicMdxFiles()) {
      const page = readPublicMdx(filePath);
      const firstParagraph = page.body
        .split(/\n\s*\n/u)
        .find((block) => !block.startsWith("import ")) ?? "";

      expect(firstParagraph).not.toContain(page.description);
    }
  });

  it("marks every public docs page as generally available", () => {
    for (const filePath of publicMdxFiles()) {
      const page = readPublicMdx(filePath);

      expect(page.status).toBe("stable");
    }
  });

  it("documents the public docs authoring model for future pages", () => {
    const agentsPath = join(publicDocsPath, "AGENTS.md");

    expect(existsSync(agentsPath)).toBe(true);

    const content = readFileSync(agentsPath, "utf8");

    expect(content).toContain("Fumadocs owns the visible page title and lede");
    expect(content).toContain("Do not start MDX pages with a duplicate `# Title`");
    expect(content).toContain("first body paragraph");
  });

  it("lists curated public docs with stable urls and metadata", () => {
    const docs = getAllPublicDocs();

    expect(docs.length).toBeGreaterThanOrEqual(8);
    expect(docs.map((doc) => doc.url)).toContain("/docs/guides/repo-config");
    expect(docs.map((doc) => doc.url)).toContain("/docs/guides/previews");
    expect(docs.map((doc) => doc.url)).toContain("/docs/self-hosting/github-app-setup");
    expect(
      docs.every((doc) => doc.title && doc.description && doc.llmSummary)
    ).toBe(true);
  });

  it("returns raw markdown for a public docs slug only", () => {
    const raw = getRawPublicDocBySlug(["guides", "repo-config"]);

    expect(raw.content).toContain("# Repo config");
    expect(raw.content).toContain("`.143/config.json`");
    expect(raw.content).not.toContain("Design: Public Docs");
  });

  it("keeps the homepage benefits bullets with team-level automation wording", () => {
    const raw = getRawPublicDocBySlug([]);

    expect(raw.content).toContain("built for engineering teams");
    expect(raw.content).toContain("defaults to team-level workflows");
    expect(raw.content).toContain("**A shared execution layer:**");
    expect(raw.content).toContain("**Team-level automation:**");
    expect(raw.content).toContain("Linear or an API");
    expect(raw.content).toContain("self-host");
    expect(raw.content).not.toContain("**Repo-specific contracts:**");
    expect(raw.content).not.toContain("## Why teams use it");
    expect(raw.content).not.toContain("**Controlled automation:**");
  });

  it("keeps preview setup and secret guidance in the public preview docs", () => {
    const raw = getRawPublicDocBySlug(["guides", "previews"]);

    expect(raw.content).toContain("## Set up the config");
    expect(raw.content).toContain("## Secrets and config");
    expect(raw.content).toContain("`preview.credentials`");
    expect(raw.content).toContain("admin-managed values");
  });

  it("publishes an agent-facing 143-tools CLI reference", () => {
    expect(referenceMeta.pages).toContain("agent-tools");

    const raw = getRawPublicDocBySlug(["reference", "agent-tools"]);

    expect(raw.content).toContain("## CLI contract");
    expect(raw.content).toContain("### `linear list_tasks`");
    expect(raw.content).toContain("`--team`");
    expect(raw.content).toContain("### `pr create`");
    expect(raw.content).toContain("### `circleci get_recent_test_failures`");
    expect(raw.content).toContain("### `logs query`");
  });

  it("generates llms.txt from the public docs index", () => {
    const pages = getAllPublicDocs() satisfies LlmsPage[];
    const llms = getPublicDocsLlmsText(pages);

    expect(llms).toContain("# 143.dev docs");
    expect(llms).toContain("- [Repo config](https://143.dev/docs/guides/repo-config)");
    expect(llms).toContain(
      "Raw Markdown: https://143.dev/docs/guides/repo-config.md"
    );
    expect(llms).not.toContain("future/85-public-docs-fumadocs");
  });
});
