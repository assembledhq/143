import { readFileSync } from "node:fs";
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

  it("does not repeat section titles as body headings on overview pages", () => {
    const tests = [
      { slug: "getting-started", title: getStartedMeta.title },
      { slug: "guides", title: guidesMeta.title },
      { slug: "self-hosting", title: selfHostingMeta.title },
      { slug: "reference", title: referenceMeta.title },
    ];

    for (const tt of tests) {
      const content = readFileSync(
        join(process.cwd(), "..", "docs", "public", tt.slug, "index.mdx"),
        "utf8"
      );

      expect(content).not.toContain(`\n# ${tt.title}\n`);
    }
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

  it("keeps preview setup and secret guidance in the public preview docs", () => {
    const raw = getRawPublicDocBySlug(["guides", "previews"]);

    expect(raw.content).toContain("## Set up the config");
    expect(raw.content).toContain("## Secrets and config");
    expect(raw.content).toContain("`preview.credentials`");
    expect(raw.content).toContain("admin-managed values");
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
