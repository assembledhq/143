import { describe, it, expect, vi, beforeEach } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { getActiveOrgId } from "@/lib/active-org";
import VerifyEmailPage from "./page";

const { replaceMock, pushMock, searchParams } = vi.hoisted(() => ({
  replaceMock: vi.fn(),
  pushMock: vi.fn(),
  searchParams: { token: "tok123" as string | null },
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock, replace: replaceMock }),
  useSearchParams: () => ({
    get: (key: string) => (key === "token" ? searchParams.token : null),
  }),
}));

beforeEach(() => {
  replaceMock.mockReset();
  pushMock.mockReset();
  searchParams.token = "tok123";
  window.sessionStorage.clear();
});

describe("VerifyEmailPage", () => {
  it("confirms the token and celebrates the auto-joined workspace", async () => {
    server.use(
      http.post("/api/v1/auth/email-verifications/confirm", () =>
        HttpResponse.json({
          data: {
            verified: true,
            joined_org: { org_id: "org-2", org_name: "Assembled", domain: "assembledhq.com" },
          },
        }),
      ),
    );

    renderWithProviders(<VerifyEmailPage />);

    expect(await screen.findByText("Welcome to Assembled")).toBeInTheDocument();
    expect(screen.getByTestId("verify-email-continue")).toHaveTextContent("Go to Assembled");
    expect(getActiveOrgId()).toBe("org-2");
  });

  it("shows plain verified state when no workspace captured the domain", async () => {
    server.use(
      http.post("/api/v1/auth/email-verifications/confirm", () =>
        HttpResponse.json({ data: { verified: true } }),
      ),
    );

    renderWithProviders(<VerifyEmailPage />);

    expect(await screen.findByText("Your email address is verified.")).toBeInTheDocument();
    expect(getActiveOrgId()).toBeNull();
  });

  it("surfaces a stale-link error with a path back to sign in", async () => {
    server.use(
      http.post("/api/v1/auth/email-verifications/confirm", () =>
        HttpResponse.json(
          { error: { code: "VERIFICATION_INVALID", message: "This verification link is invalid or has expired. Request a new one and try again." } },
          { status: 410 },
        ),
      ),
    );

    renderWithProviders(<VerifyEmailPage />);

    expect(await screen.findByText(/invalid or has expired/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Go to sign in" })).toBeInTheDocument();
  });

  it("rejects a missing token without calling the API", async () => {
    searchParams.token = null;

    renderWithProviders(<VerifyEmailPage />);

    expect(await screen.findByText("No verification token provided.")).toBeInTheDocument();
  });
});
