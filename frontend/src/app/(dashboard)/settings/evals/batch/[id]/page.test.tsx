import { describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import BatchDetailPage from "./page";

vi.mock("next/link", () => ({
  default: ({ children, href, ...props }: React.ComponentProps<"a"> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

vi.mock("next/navigation", () => ({
  useParams: () => ({ id: "batch-1" }),
}));

vi.mock("@/lib/use-resource-sse", async () => {
  const actual = await vi.importActual<typeof import("@/lib/use-resource-sse")>("@/lib/use-resource-sse");
  return {
    ...actual,
    useResourceSSE: () => ({ healthy: true }),
  };
});

describe("BatchDetailPage", () => {
  it("updates the browser tab title with the eval batch name", async () => {
    server.use(
      http.get("*/api/v1/evals/batch/batch-1", () => HttpResponse.json({
        data: {
          id: "batch-1",
          org_id: "org-1",
          name: "Claude versus Codex checkout run",
          status: "completed",
          task_count: 0,
          run_count: 0,
          runs: [],
          created_at: "2026-01-01T00:00:00Z",
        },
      })),
    );

    renderWithProviders(<BatchDetailPage />);

    await waitFor(() => {
      expect(document.title).toBe("143 | Claude versus Codex checkout run");
    });
  });
});
