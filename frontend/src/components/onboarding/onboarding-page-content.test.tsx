import { describe, it, expect, vi, beforeEach } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { OnboardingPageContent } from "./onboarding-page-content";

const { replaceMock } = vi.hoisted(() => ({ replaceMock: vi.fn() }));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: replaceMock }),
}));

beforeEach(() => {
  replaceMock.mockReset();
});

function mockMe(user: Record<string, unknown>) {
  server.use(
    http.get("/api/v1/auth/me", () =>
      HttpResponse.json({
        data: {
          id: "user-1",
          org_id: "org-1",
          name: "Bob",
          role: "admin",
          created_at: new Date().toISOString(),
          ...user,
        },
      }),
    ),
  );
}

describe("OnboardingPageContent verify-email banner", () => {
  it("shows the banner with the user's address for unverified password accounts", async () => {
    mockMe({ email: "bob@assembledhq.com", email_verified: false });

    renderWithProviders(<OnboardingPageContent />);

    expect(await screen.findByTestId("onboarding-verify-email-banner")).toBeInTheDocument();
    expect(screen.getByText("bob@assembledhq.com")).toBeInTheDocument();
  });

  it("resend button calls the verification endpoint", async () => {
    mockMe({ email: "bob@assembledhq.com", email_verified: false });
    let sent = false;
    server.use(
      http.post("/api/v1/auth/email-verifications", () => {
        sent = true;
        return new HttpResponse(null, { status: 202 });
      }),
    );

    renderWithProviders(<OnboardingPageContent />);
    await userEvent.click(await screen.findByTestId("onboarding-verify-email-resend"));

    await waitFor(() => {
      expect(sent).toBe(true);
    });
  });

  it("hides the banner for OAuth accounts even when unverified", async () => {
    // GitHub identity present: the provider attests the email on every
    // login, so the email-verification flow does not apply.
    mockMe({ email: "42+bob@users.noreply.github.com", email_verified: false, github_id: 42 });

    renderWithProviders(<OnboardingPageContent />);

    await waitFor(() => {
      expect(screen.getByText(/Autopilot needs a few connections/)).toBeInTheDocument();
    });
    expect(screen.queryByTestId("onboarding-verify-email-banner")).not.toBeInTheDocument();
  });

  it("hides the banner once the address is verified", async () => {
    mockMe({ email: "bob@assembledhq.com", email_verified: true });

    renderWithProviders(<OnboardingPageContent />);

    await waitFor(() => {
      expect(screen.getByText(/Autopilot needs a few connections/)).toBeInTheDocument();
    });
    expect(screen.queryByTestId("onboarding-verify-email-banner")).not.toBeInTheDocument();
  });
});
