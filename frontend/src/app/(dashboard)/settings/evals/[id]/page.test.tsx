import { describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import EvalTaskDetailPage from "./page";

vi.mock("next/link", () => ({
  default: ({ children, href, ...props }: React.ComponentProps<"a"> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

vi.mock("next/navigation", () => ({
  useParams: () => ({ id: "eval-1" }),
  useRouter: () => ({ push: vi.fn() }),
}));

describe("EvalTaskDetailPage", () => {
  it("updates the browser tab title with the eval task name", async () => {
    server.use(
      http.get("*/api/v1/evals/tasks/eval-1", () => HttpResponse.json({
        data: {
          id: "eval-1",
          org_id: "org-1",
          repo_id: "repo-1",
          name: "Checkout regression eval",
          description: "Verify checkout fixes",
          base_commit_sha: "abcdef1234567890",
          issue_description: "Checkout fails after deploy",
          scoring_criteria: [],
          pass_threshold: 0.8,
          source: "manual",
          complexity: "simple",
          snapshot_broken: false,
          tags: [],
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
      })),
      http.get("*/api/v1/evals/tasks/eval-1/runs*", () => HttpResponse.json({ data: [], meta: {} })),
    );

    renderWithProviders(<EvalTaskDetailPage />);

    await waitFor(() => {
      expect(document.title).toBe("143 | Checkout regression eval");
    });
  });
});
