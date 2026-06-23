import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { MarkdownContent } from "./markdown";

describe("MarkdownContent", () => {
  it("renders plain text", () => {
    render(<MarkdownContent content="Hello world" />);
    expect(screen.getByText("Hello world")).toBeInTheDocument();
  });

  it("renders bold and italic text", () => {
    render(<MarkdownContent content="This is **bold** and *italic*" />);
    expect(screen.getByText("bold").tagName).toBe("STRONG");
    expect(screen.getByText("italic").tagName).toBe("EM");
  });

  it("renders inline code", () => {
    render(<MarkdownContent content="Run `npm install` to start" />);
    const code = screen.getByText("npm install");
    expect(code.tagName).toBe("CODE");
    // Inline code should NOT be inside a <pre>
    expect(code.closest("pre")).toBeNull();
  });

  it("wraps long inline code without overflowing its line box", () => {
    render(
      <MarkdownContent content="Use `luxonDateTimeToMomentInTimezone(adjust_start.toJSDate(), UserManager.timezone())` here" />
    );

    const code = screen.getByText(
      "luxonDateTimeToMomentInTimezone(adjust_start.toJSDate(), UserManager.timezone())"
    );
    expect(code).toHaveClass("break-all", {
      exact: false,
    });
    expect(code).toHaveClass("box-decoration-clone", {
      exact: false,
    });
    expect(code).toHaveClass("leading-relaxed", {
      exact: false,
    });
  });

  it("renders fenced code blocks with language", () => {
    const md = "```js\nconsole.log('hi');\n```";
    render(<MarkdownContent content={md} />);
    const code = screen.getByText("console.log('hi');");
    expect(code.tagName).toBe("CODE");
    expect(code.closest("pre")).not.toBeNull();
  });

  it("renders fenced code blocks without language", () => {
    const md = "```\nplain code block\n```";
    render(<MarkdownContent content={md} />);
    const code = screen.getByText("plain code block");
    expect(code.tagName).toBe("CODE");
    expect(code.closest("pre")).not.toBeNull();
  });

  it("renders unordered lists", () => {
    const md = "- Item one\n- Item two\n- Item three";
    render(<MarkdownContent content={md} />);
    expect(screen.getByText("Item one")).toBeInTheDocument();
    expect(screen.getByText("Item two")).toBeInTheDocument();
    const list = screen.getByText("Item one").closest("ul");
    expect(list).not.toBeNull();
  });

  it("renders ordered lists", () => {
    const md = "1. First\n2. Second";
    render(<MarkdownContent content={md} />);
    const list = screen.getByText("First").closest("ol");
    expect(list).not.toBeNull();
  });

  it("preserves ordered list start numbers", () => {
    const md = "2. Second\n3. Third";
    render(<MarkdownContent content={md} />);
    const list = screen.getByText("Second").closest("ol");
    expect(list).toHaveAttribute("start", "2");
  });

  it("renders headings", () => {
    const md = "# Title\n## Subtitle\n### Section";
    render(<MarkdownContent content={md} />);
    expect(screen.getByText("Title").tagName).toBe("H1");
    expect(screen.getByText("Subtitle").tagName).toBe("H2");
    expect(screen.getByText("Section").tagName).toBe("H3");
  });

  it("renders links with target=_blank", () => {
    const md = "[click here](https://example.com)";
    render(<MarkdownContent content={md} />);
    const link = screen.getByText("click here");
    expect(link.tagName).toBe("A");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
  });

  it("renders GFM tables", () => {
    const md = "| Col A | Col B |\n| --- | --- |\n| val1 | val2 |";
    render(<MarkdownContent content={md} />);
    expect(screen.getByText("Col A").tagName).toBe("TH");
    expect(screen.getByText("val1").tagName).toBe("TD");
  });

  it("renders blockquotes", () => {
    render(<MarkdownContent content="> This is a quote" />);
    const bq = screen.getByText("This is a quote").closest("blockquote");
    expect(bq).not.toBeNull();
  });

  it("applies custom className", () => {
    const { container } = render(
      <MarkdownContent content="test" className="my-custom-class" />
    );
    expect(container.firstElementChild).toHaveClass("my-custom-class");
  });
});
