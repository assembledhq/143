import { act } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import CreateEvalTaskPage from "./page";

const pushMock = vi.fn();

vi.mock("next/link", () => ({
  default: ({ children, href, ...props }: React.ComponentProps<"a"> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock }),
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

describe("CreateEvalTaskPage", () => {
  beforeEach(() => {
    pushMock.mockReset();
  });

  it("keeps the create button loading after eval creation succeeds while navigation is pending", async () => {
    const user = userEvent.setup();
    const createEval = deferred<Response>();

    server.use(
      http.get("*/api/v1/repositories", () => HttpResponse.json({
        data: [
          {
            id: "repo-1",
            org_id: "org-1",
            full_name: "acme/api",
            default_branch: "main",
            github_id: 1,
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
        ],
        meta: {},
      })),
      http.post("*/api/v1/evals/tasks", async () => createEval.promise),
    );

    renderWithProviders(<CreateEvalTaskPage />);

    await user.click(screen.getByRole("combobox"));
    await user.click(await screen.findByText("acme/api"));
    await user.type(screen.getByLabelText("Base commit SHA"), "abcdef1234567890");
    await user.click(screen.getByRole("button", { name: "Next" }));

    await user.type(await screen.findByLabelText("Name"), "Checkout regression");
    await user.type(screen.getByLabelText("Description"), "Verifies checkout fixes");
    await user.type(screen.getByLabelText("Issue description"), "Checkout fails after deploy");
    await user.click(screen.getByRole("button", { name: "Next" }));

    await user.click(screen.getByRole("button", { name: "Next" }));

    const createButton = screen.getByRole("button", { name: "Create eval task" });
    await user.click(createButton);

    expect(createButton).toBeDisabled();

    await act(async () => {
      createEval.resolve(HttpResponse.json({ data: { id: "eval-1" } }));
      await createEval.promise;
    });

    await waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith("/settings/evals/eval-1");
    });
    expect(createButton).toBeDisabled();
    expect(createButton).toHaveTextContent("Creating...");
  });
});
