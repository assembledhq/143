import React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { ManualSessionCreatePageContent } from "./manual-session-create-page-content";

const mocks = vi.hoisted(() => ({
  settingsGetMock: vi.fn().mockResolvedValue({
    data: {
      name: "Test Org",
      settings: {
        default_agent_type: "codex",
        default_llm_model: "gpt-5.4-mini",
      },
    },
  }),
  repositoriesListMock: vi.fn().mockResolvedValue({
    data: [
      {
        id: "repo-1",
        name: "test-repo",
        full_name: "org/test-repo",
        default_branch: "main",
        integration_id: "int-1",
      },
    ],
  }),
  llmModelsMock: vi.fn().mockResolvedValue({
    data: { openai: ["gpt-5.4", "gpt-5.4-mini"] },
  }),
  codingCredentialsListMock: vi.fn().mockResolvedValue({
    data: [
      {
        id: "cc-codex",
        org_id: "org-1",
        user_id: "user-1",
        scope: "personal",
        priority: 1,
        agent: "codex",
        auth_type: "api_key",
        provider: "openai",
        label: "Codex API key",
        status: "healthy",
        is_default: true,
        created_at: "2026-01-01T00:00:00Z",
        updated_at: "2026-01-01T00:00:00Z",
      },
    ],
  }),
  codexAuthStatusMock: vi.fn().mockResolvedValue({ data: { status: "completed" } }),
  authMeMock: vi.fn().mockResolvedValue({
    data: {
      id: "user-1",
      org_id: "org-1",
      email: "alice@example.com",
      name: "Alice Smith",
      role: "admin",
      settings: {},
      created_at: "2026-01-01T00:00:00Z",
    },
  }),
  integrationsListMock: vi.fn().mockResolvedValue({ data: [] }),
  sessionComposerFilesMock: vi.fn().mockResolvedValue({ data: [] }),
  sessionComposerSlashCommandsMock: vi.fn().mockResolvedValue({ groups: [] }),
  createSessionMock: vi.fn().mockResolvedValue({ data: { id: "new-sess" } }),
  addOptimisticSessionMock: vi.fn().mockReturnValue("optimistic-1"),
  removeOptimisticSessionMock: vi.fn(),
  markOptimisticResolvedMock: vi.fn(),
  searchParamGetMock: vi.fn<(key: string) => string | null>().mockImplementation(() => null),
  branchPickerRenderCount: { count: 0 },
}));

vi.mock("@/lib/api", () => ({
  api: {
    settings: {
      get: mocks.settingsGetMock,
      getLLMModels: mocks.llmModelsMock,
    },
    repositories: {
      list: mocks.repositoriesListMock,
      branches: vi.fn().mockResolvedValue({ data: [{ name: "main", protected: true }] }),
    },
    sessionComposer: {
      files: mocks.sessionComposerFilesMock,
      slashCommands: mocks.sessionComposerSlashCommandsMock,
    },
    uploads: {
      upload: vi.fn(),
    },
    codingCredentials: {
      list: mocks.codingCredentialsListMock,
    },
    codexAuth: {
      status: mocks.codexAuthStatusMock,
    },
    auth: {
      me: mocks.authMeMock,
    },
    integrations: {
      list: mocks.integrationsListMock,
    },
    sessions: {
      createManual: mocks.createSessionMock,
    },
  },
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
    prefetch: vi.fn(),
  }),
  useSearchParams: () => ({
    get: mocks.searchParamGetMock,
  }),
}));

vi.mock("@/components/no-repos-warning", () => ({
  NoReposWarning: () => <div data-testid="no-repos-warning" />,
}));

vi.mock("@/components/branch-picker", () => ({
  BranchPicker: ({ label }: { label: string }) => {
    mocks.branchPickerRenderCount.count += 1;
    return (
      <button type="button" data-testid="branch-picker-mock" aria-label={label}>
        {label}
      </button>
    );
  },
}));

vi.mock("@/contexts/optimistic-sessions", () => ({
  useOptimisticSessions: () => ({
    addOptimisticSession: mocks.addOptimisticSessionMock,
    removeOptimisticSession: mocks.removeOptimisticSessionMock,
    markOptimisticResolved: mocks.markOptimisticResolvedMock,
  }),
  useOptimisticSessionsSafe: () => ({
    addOptimisticSession: mocks.addOptimisticSessionMock,
    removeOptimisticSession: mocks.removeOptimisticSessionMock,
    markOptimisticResolved: mocks.markOptimisticResolvedMock,
  }),
  OptimisticSessionsProvider: ({ children }: { children: React.ReactNode }) => children,
}));

describe("ManualSessionCreatePageContent performance", () => {
  beforeEach(() => {
    Object.values(mocks).forEach((value) => {
      if (typeof value === "function" && "mockClear" in value) {
        value.mockClear();
      }
    });
    mocks.branchPickerRenderCount.count = 0;
    mocks.searchParamGetMock.mockImplementation(() => null);
  });

  it("does not rerender repository controls while typing in the prompt", async () => {
    const user = userEvent.setup();

    renderWithProviders(<ManualSessionCreatePageContent />);

    const textarea = await screen.findByLabelText("Manual session prompt");
    await screen.findByTestId("branch-picker-mock");

    mocks.branchPickerRenderCount.count = 0;

    await user.type(textarea, "Lag");

    expect(textarea).toHaveValue("Lag");
    expect(mocks.branchPickerRenderCount.count).toBe(0);
  });
});
