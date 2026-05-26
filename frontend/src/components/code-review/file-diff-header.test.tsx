import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { FileDiffHeader } from "./file-diff-header";


describe("FileDiffHeader", () => {
  it("renders the file path", () => {
    render(<FileDiffHeader filePath="src/app.ts" added={3} removed={1} />);
    expect(screen.getByText("src/app.ts")).toBeInTheDocument();
  });

  it("uses attached diff-surface styling without an independent sticky shadow", () => {
    const { container } = render(<FileDiffHeader filePath="src/app.ts" added={3} removed={1} />);
    const header = container.firstElementChild;

    expect(header).toHaveClass("bg-card/95");
    expect(header).toHaveClass("border-b");
    expect(header).toHaveClass("shadow-none");
  });

  it("renders diff stats badge", () => {
    render(<FileDiffHeader filePath="src/app.ts" added={5} removed={2} />);
    expect(screen.getByText("+5")).toBeInTheDocument();
    expect(screen.getByText("-2")).toBeInTheDocument();
  });

  it("shows copy file path button", () => {
    render(<FileDiffHeader filePath="src/app.ts" added={1} removed={0} />);
    expect(screen.getByTitle("Copy file path")).toBeInTheDocument();
  });

  it("clicking copy button does not throw", async () => {
    const user = userEvent.setup();
    render(<FileDiffHeader filePath="src/app.ts" added={1} removed={0} />);
    // Just verify clicking doesn't throw (clipboard may not be available in jsdom)
    await expect(user.click(screen.getByTitle("Copy file path"))).resolves.not.toThrow();
  });

  it("shows browse button when onBrowseFile is provided", () => {
    render(
      <FileDiffHeader filePath="src/app.ts" added={0} removed={0} onBrowseFile={vi.fn()} />
    );
    expect(screen.getByTitle("Browse in repository explorer")).toBeInTheDocument();
  });

  it("does not show browse button when onBrowseFile is absent", () => {
    render(<FileDiffHeader filePath="src/app.ts" added={0} removed={0} />);
    expect(screen.queryByTitle("Browse in repository explorer")).not.toBeInTheDocument();
  });

  it("calls onBrowseFile with file path on click", async () => {
    const onBrowseFile = vi.fn();
    const user = userEvent.setup();
    render(
      <FileDiffHeader filePath="src/app.ts" added={0} removed={0} onBrowseFile={onBrowseFile} />
    );
    await user.click(screen.getByTitle("Browse in repository explorer"));
    expect(onBrowseFile).toHaveBeenCalledWith("src/app.ts");
  });
});
