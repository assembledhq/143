import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock shiki before importing the module
vi.mock("shiki", () => {
  const mockHighlighter = {
    loadLanguage: vi.fn().mockResolvedValue(undefined),
    codeToTokens: vi.fn().mockReturnValue({
      tokens: [
        [{ content: "const", color: "#ff0000" }, { content: " x", color: undefined }],
        [{ content: "= 1", color: "#00ff00" }],
      ],
    }),
  };
  return {
    createHighlighter: vi.fn().mockResolvedValue(mockHighlighter),
    __mockHighlighter: mockHighlighter,
  };
});

// Must import after mock setup
const { highlightLines } = await import("./syntax-highlighter");
const shiki = await import("shiki");
const mockHighlighter = (shiki as Record<string, unknown>).__mockHighlighter as {
  loadLanguage: ReturnType<typeof vi.fn>;
  codeToTokens: ReturnType<typeof vi.fn>;
};

describe("highlightLines", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("returns empty array for empty input", async () => {
    const result = await highlightLines([], "typescript");
    expect(result).toEqual([]);
  });

  it("returns highlighted HTML spans for tokens with colors", async () => {
    const result = await highlightLines(["const x", "= 1"], "typescript");
    expect(result[0]).toContain('<span style="color:#ff0000">const</span>');
    expect(result[0]).toContain(" x");
    expect(result[1]).toContain('<span style="color:#00ff00">= 1</span>');
  });

  it("escapes HTML entities in token content", async () => {
    mockHighlighter.codeToTokens.mockReturnValueOnce({
      tokens: [[{ content: "<div>", color: "#000" }]],
    });
    const result = await highlightLines(["<div>"], "html");
    expect(result[0]).toContain("&lt;div&gt;");
    expect(result[0]).not.toContain("<div>");
  });

  it("loads language lazily on first call", async () => {
    await highlightLines(["code"], "rust");
    expect(mockHighlighter.loadLanguage).toHaveBeenCalledWith("rust");
  });

  it("falls back to escaped HTML when loadLanguage throws", async () => {
    mockHighlighter.loadLanguage.mockRejectedValueOnce(new Error("unknown lang"));
    const result = await highlightLines(["<b>bold</b>"], "fakeLang123");
    expect(result[0]).toBe("&lt;b&gt;bold&lt;/b&gt;");
  });

  it("falls back to escaped HTML when codeToTokens throws", async () => {
    mockHighlighter.codeToTokens.mockImplementationOnce(() => {
      throw new Error("tokenization error");
    });
    const result = await highlightLines(["hello & world"], "typescript");
    expect(result[0]).toBe("hello &amp; world");
  });

  it("passes theme parameter to codeToTokens", async () => {
    await highlightLines(["x"], "typescript", "github-light");
    expect(mockHighlighter.codeToTokens).toHaveBeenCalledWith("x", {
      lang: "typescript",
      theme: "github-light",
    });
  });
});
