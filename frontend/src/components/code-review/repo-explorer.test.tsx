import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { RepoExplorer } from "./repo-explorer";
import type { DiffFile } from "@/lib/diff-parser";

// Mock diff files for testing changed-line indicators
const mockDiffFiles: DiffFile[] = [
  {
    oldPath: "src/main.ts",
    newPath: "src/main.ts",
    hunks: [
      {
        oldStart: 5,
        oldCount: 3,
        newStart: 5,
        newCount: 4,
        header: "@@ -5,3 +5,4 @@",
        lines: [
          {
            type: "context",
            content: "const x = 1;",
            oldLineNumber: 5,
            newLineNumber: 5,
          },
          {
            type: "remove",
            content: "const y = 2;",
            oldLineNumber: 6,
            newLineNumber: null,
          },
          {
            type: "add",
            content: 'const y = "updated";',
            oldLineNumber: null,
            newLineNumber: 6,
          },
          {
            type: "add",
            content: "const z = 3;",
            oldLineNumber: null,
            newLineNumber: 7,
          },
          {
            type: "context",
            content: "export { x, y };",
            oldLineNumber: 7,
            newLineNumber: 8,
          },
        ],
      },
    ],
    stats: { added: 2, removed: 1 },
    language: "typescript",
  },
];

describe("RepoExplorer", () => {
  it("renders the back button and breadcrumb", async () => {
    const onBack = vi.fn();

    renderWithProviders(
      <RepoExplorer
        sessionId="session-abcdef12-3456-7890"
        diffFiles={mockDiffFiles}
        onBack={onBack}
      />
    );

    expect(screen.getByText("Back to diff")).toBeInTheDocument();
    expect(screen.getByText("root")).toBeInTheDocument();
  });

  it("calls onBack when back button is clicked", async () => {
    const onBack = vi.fn();
    const user = userEvent.setup();

    renderWithProviders(
      <RepoExplorer
        sessionId="session-abcdef12-3456-7890"
        diffFiles={mockDiffFiles}
        onBack={onBack}
      />
    );

    await user.click(screen.getByText("Back to diff"));
    expect(onBack).toHaveBeenCalledOnce();
  });

  it("shows loading state while fetching directory listing", () => {
    renderWithProviders(
      <RepoExplorer
        sessionId="session-abcdef12-3456-7890"
        diffFiles={[]}
        onBack={vi.fn()}
      />
    );

    // The "Select a file to view its content" placeholder should show
    expect(
      screen.getByText("Select a file to view its content")
    ).toBeInTheDocument();
  });

  it("loads directory listing from API", async () => {
    renderWithProviders(
      <RepoExplorer
        sessionId="session-abcdef12-3456-7890"
        diffFiles={[]}
        onBack={vi.fn()}
      />
    );

    // Mock returns: src (dir), internal (dir), main.go (file), README.md (file)
    await waitFor(() => {
      expect(screen.getByText("src")).toBeInTheDocument();
    });
    expect(screen.getByText("internal")).toBeInTheDocument();
    expect(screen.getByText("main.go")).toBeInTheDocument();
    expect(screen.getByText("README.md")).toBeInTheDocument();
  });

  it("loads file content when a file is selected", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <RepoExplorer
        sessionId="session-abcdef12-3456-7890"
        diffFiles={[]}
        onBack={vi.fn()}
      />
    );

    // Wait for directory listing
    await waitFor(() => {
      expect(screen.getByText("main.go")).toBeInTheDocument();
    });

    // Click on a file
    await user.click(screen.getByText("main.go"));

    // Wait for file content to load (mock returns "// Mock file content\nexport function hello()...")
    await waitFor(() => {
      expect(screen.getByText(/Mock file content/)).toBeInTheDocument();
    });
  });

  it("opens to initial path when provided", async () => {
    renderWithProviders(
      <RepoExplorer
        sessionId="session-abcdef12-3456-7890"
        diffFiles={mockDiffFiles}
        onBack={vi.fn()}
        initialPath="src/main.ts"
      />
    );

    // Should show breadcrumb with src / main.ts
    await waitFor(() => {
      expect(screen.getByText("main.ts")).toBeInTheDocument();
    });

    // Should load the file content
    await waitFor(() => {
      expect(screen.getByText(/Mock file content/)).toBeInTheDocument();
    });
  });

  it("navigates breadcrumb on click", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <RepoExplorer
        sessionId="session-abcdef12-3456-7890"
        diffFiles={[]}
        onBack={vi.fn()}
        initialPath="src/components/app.tsx"
      />
    );

    // Should have breadcrumb parts
    await waitFor(() => {
      expect(screen.getByText("app.tsx")).toBeInTheDocument();
    });

    // Click "root" in breadcrumb to go back to root
    await user.click(screen.getByText("root"));

    // Should show placeholder for file content
    await waitFor(() => {
      expect(
        screen.getByText("Select a file to view its content")
      ).toBeInTheDocument();
    });
  });

  it("shows go-up (..) button when in a subdirectory", async () => {
    renderWithProviders(
      <RepoExplorer
        sessionId="session-abcdef12-3456-7890"
        diffFiles={[]}
        onBack={vi.fn()}
        initialPath="src/main.ts"
      />
    );

    // Should show the ".." go-up button since we're in "src" directory
    await waitFor(() => {
      expect(screen.getByText("..")).toBeInTheDocument();
    });
  });
});
