import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, waitFor } from "@/test/test-utils";
import LLMPage from "./page";

const {
  settingsGetMock,
  credentialsListMock,
  llmModelsMock,
} = vi.hoisted(() => ({
  settingsGetMock: vi.fn().mockResolvedValue({
    data: {
      name: "Test Org",
      settings: {
        default_llm_model: "gpt-4o",
      },
    },
  }),
  credentialsListMock: vi.fn().mockResolvedValue({
    data: [],
  }),
  llmModelsMock: vi.fn().mockResolvedValue({
    data: {
      openai: ["gpt-4o", "gpt-4o-mini"],
      anthropic: ["claude-sonnet-4-20250514"],
    },
  }),
}));

vi.mock("@/lib/api", () => ({
  api: {
    settings: {
      get: settingsGetMock,
      getLLMModels: llmModelsMock,
      update: vi.fn().mockResolvedValue({}),
    },
    credentials: {
      list: credentialsListMock,
    },
  },
}));

vi.mock("@/lib/errors", () => ({
  captureError: vi.fn(),
}));

describe("LLMPage", () => {
  beforeEach(() => {
    settingsGetMock.mockClear();
    credentialsListMock.mockClear();
    llmModelsMock.mockClear();
  });

  it("renders the page header", async () => {
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByText(/LLM/i)).toBeInTheDocument();
    });
  });

  it("renders provider selection area", async () => {
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(settingsGetMock).toHaveBeenCalled();
    });
  });
});
