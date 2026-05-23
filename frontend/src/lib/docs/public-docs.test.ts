import { describe, expect, it } from "vitest";
import {
  DOCS_BASE_PATH,
  getAllPublicDocs,
  getRawPublicDocBySlug,
  getPublicDocsLlmsText,
} from "./public-docs";

describe("public docs source", () => {
  it("uses docs/public as the only public content root", () => {
    expect(DOCS_BASE_PATH).toBe("docs/public");
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

  it("generates llms.txt from the public docs index", () => {
    const llms = getPublicDocsLlmsText();

    expect(llms).toContain("# 143.dev docs");
    expect(llms).toContain("- [Repo config](https://143.dev/docs/guides/repo-config)");
    expect(llms).toContain(
      "Raw Markdown: https://143.dev/docs/guides/repo-config.md"
    );
    expect(llms).not.toContain("future/85-public-docs-fumadocs");
  });
});
