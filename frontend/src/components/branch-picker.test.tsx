import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { BranchPicker } from "./branch-picker";

const mocks = vi.hoisted(() => ({
  branchesMock: vi.fn(),
}));

vi.mock("@/lib/api", () => ({
  api: {
    repositories: {
      branches: mocks.branchesMock,
    },
  },
}));

describe("BranchPicker", () => {
  beforeEach(() => {
    mocks.branchesMock.mockReset();
  });

  it("searches branches server-side and applies the selected branch", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();

    const allBranches = [
      { name: "main", protected: true },
      { name: "release/2026.04", protected: true },
      { name: "feature/smart-picker", protected: false },
    ];
    // Mirror the backend: the search query filters server-side, so the picker
    // only ever renders what the request returned for the current query.
    mocks.branchesMock.mockImplementation((_id: string, query?: string) =>
      Promise.resolve({
        data: query
          ? allBranches.filter((b) => b.name.includes(query))
          : allBranches,
      }),
    );

    renderWithProviders(
      <BranchPicker
        repositoryId="repo-1"
        value="main"
        defaultBranch="main"
        onValueChange={onValueChange}
        label="Base branch"
      />,
    );

    await user.click(screen.getByRole("button", { name: "Base branch" }));

    expect(await screen.findByPlaceholderText("Search branches...")).toBeInTheDocument();
    expect(await screen.findByText("feature/smart-picker")).toBeInTheDocument();

    await user.type(screen.getByPlaceholderText("Search branches..."), "release");

    // Once the debounced query resolves, the non-matching branch is gone.
    await waitFor(() =>
      expect(screen.queryByText("feature/smart-picker")).not.toBeInTheDocument(),
    );
    expect(screen.getByText("release/2026.04")).toBeInTheDocument();
    expect(mocks.branchesMock).toHaveBeenCalledWith("repo-1", "release");

    await user.click(screen.getByText("release/2026.04"));

    expect(onValueChange).toHaveBeenCalledWith("release/2026.04");
  });

  it("shows a load error state instead of a freeform branch input", async () => {
    const user = userEvent.setup();

    mocks.branchesMock.mockRejectedValue(new Error("boom"));

    renderWithProviders(
      <BranchPicker
        repositoryId="repo-1"
        value="main"
        defaultBranch="main"
        onValueChange={vi.fn()}
        label="Base branch"
      />,
    );

    await user.click(screen.getByRole("button", { name: "Base branch" }));

    expect(await screen.findByText("Could not load branches.")).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: "Base branch" })).not.toBeInTheDocument();
  });

  it("anchors empty branch results to the trigger width", async () => {
    const user = userEvent.setup();

    mocks.branchesMock.mockResolvedValue({ data: [] });

    renderWithProviders(
      <BranchPicker
        repositoryId="repo-1"
        value=""
        defaultBranch="main"
        onValueChange={vi.fn()}
        label="Base branch"
      />,
    );

    await user.click(screen.getByRole("button", { name: "Base branch" }));

    const emptyState = await screen.findByText("No branches found.");
    const content = emptyState.closest('[data-slot="popover-content"]');
    expect(content).toHaveClass("w-[var(--radix-popover-trigger-width)]");
  });

  it("refreshes branches when the picker is reopened", async () => {
    const user = userEvent.setup();

    mocks.branchesMock
      .mockResolvedValueOnce({
        data: [{ name: "main", protected: true }],
      })
      .mockResolvedValueOnce({
        data: [
          { name: "main", protected: true },
          { name: "feature/just-created", protected: false },
        ],
      });

    renderWithProviders(
      <BranchPicker
        repositoryId="repo-1"
        value="main"
        defaultBranch="main"
        onValueChange={vi.fn()}
        label="Base branch"
      />,
    );

    await user.click(screen.getByRole("button", { name: "Base branch" }));
    expect(await screen.findByRole("option", { name: "main" })).toBeInTheDocument();
    expect(screen.queryByText("feature/just-created")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Base branch" }));
    await user.click(screen.getByRole("button", { name: "Base branch" }));

    expect(await screen.findByText("feature/just-created")).toBeInTheDocument();
  });
});
