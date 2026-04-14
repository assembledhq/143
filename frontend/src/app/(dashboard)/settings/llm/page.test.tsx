import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, waitFor, userEvent } from "@/test/test-utils";
import LLMPage from "./page";

const {
  settingsGetMock,
  credentialsListMock,
  credentialsUpdateMock,
  credentialsDeleteMock,
  settingsUpdateMock,
  llmModelsMock,
  llmDefaultsMock,
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
  credentialsUpdateMock: vi.fn().mockResolvedValue({}),
  credentialsDeleteMock: vi.fn().mockResolvedValue({}),
  settingsUpdateMock: vi.fn().mockResolvedValue({}),
  llmModelsMock: vi.fn().mockResolvedValue({
    data: {
      openai: ["gpt-4o", "gpt-4o-mini"],
      anthropic: ["claude-sonnet-4-20250514"],
    },
  }),
  llmDefaultsMock: vi.fn().mockResolvedValue({
    data: { default_llm_model: "gpt-4o", available_providers: ["openai"] },
  }),
}));

vi.mock("@/lib/api", () => ({
  api: {
    settings: {
      get: settingsGetMock,
      getLLMModels: llmModelsMock,
      getLLMDefaults: llmDefaultsMock,
      update: settingsUpdateMock,
    },
    credentials: {
      list: credentialsListMock,
      update: credentialsUpdateMock,
      delete: credentialsDeleteMock,
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
    credentialsUpdateMock.mockClear();
    credentialsDeleteMock.mockClear();
    settingsUpdateMock.mockClear();
    llmModelsMock.mockClear();
    llmDefaultsMock.mockClear();
  });

  it("renders the page header", async () => {
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: /LLM/i })).toBeInTheDocument();
    });
  });

  it("renders provider selection area", async () => {
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(settingsGetMock).toHaveBeenCalled();
    });
  });

  it("renders agent credentials section", async () => {
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByText("Agent credentials")).toBeInTheDocument();
    });
  });

  it("renders model selection section", async () => {
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByText("Default LLM model")).toBeInTheDocument();
    });
  });

  it("renders platform intelligence section", async () => {
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByText("Platform intelligence")).toBeInTheDocument();
    });
  });

  it("renders configured badge when credentials exist", async () => {
    credentialsListMock.mockResolvedValue({
      data: [
        {
          provider: "openai",
          configured: true,
          masked_key: "sk-...abc",
        },
      ],
    });

    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByText("Configured")).toBeInTheDocument();
    });
  });

  it("renders reasoning effort selector", async () => {
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByText("Reasoning effort")).toBeInTheDocument();
    });
  });

  it("renders save model button", async () => {
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Save model/i })).toBeInTheDocument();
    });
  });

  it("renders provider card names and descriptions for all providers", async () => {
    renderWithProviders(<LLMPage />);

    // The component fires multiple async queries that trigger re-renders,
    // which can cause intermediate states where card content is incomplete.
    // Wait for ALL expected texts to be present in a single check to avoid
    // catching a transient mid-render state.
    await waitFor(() => {
      for (const text of [
        "Anthropic",
        "OpenAI",
        "OpenRouter",
        "Claude models (Opus, Sonnet, Haiku)",
        "GPT-4o and O3 models",
        "Access all models with a single key",
      ]) {
        expect(screen.getByText(text)).toBeInTheDocument();
      }
    }, { timeout: 3000 });
  });

  it("renders input fields with correct placeholders for each provider", async () => {
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByPlaceholderText("sk-ant-...")).toBeInTheDocument();
    });
    expect(screen.getByPlaceholderText("sk-...")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("sk-or-...")).toBeInTheDocument();
  });

  it("renders save key buttons disabled when inputs are empty", async () => {
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByText("Anthropic")).toBeInTheDocument();
    });

    const saveKeyButtons = screen.getAllByRole("button", { name: /Save key/i });
    for (const btn of saveKeyButtons) {
      expect(btn).toBeDisabled();
    }
  });

  it("enables save key button when typing a key", async () => {
    const user = userEvent.setup();
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByPlaceholderText("sk-ant-...")).toBeInTheDocument();
    });

    const input = screen.getByPlaceholderText("sk-ant-...");
    await user.type(input, "sk-ant-test123");

    const saveKeyButtons = screen.getAllByRole("button", { name: /Save key/i });
    // The first provider (Anthropic) button should now be enabled
    expect(saveKeyButtons[0]).toBeEnabled();
  });

  it("calls credentials.update when save key is clicked", async () => {
    const user = userEvent.setup();
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByPlaceholderText("sk-ant-...")).toBeInTheDocument();
    });

    const input = screen.getByPlaceholderText("sk-ant-...");
    await user.type(input, "sk-ant-test123");

    const saveKeyButtons = screen.getAllByRole("button", { name: /Save key/i });
    await user.click(saveKeyButtons[0]);

    await waitFor(() => {
      expect(credentialsUpdateMock).toHaveBeenCalledWith("anthropic", { api_key: "sk-ant-test123" });
    });
  });

  it("sends api_type for openai provider when saving key", async () => {
    const user = userEvent.setup();
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByPlaceholderText("sk-...")).toBeInTheDocument();
    });

    const input = screen.getByPlaceholderText("sk-...");
    await user.type(input, "sk-openai-key");

    const saveKeyButtons = screen.getAllByRole("button", { name: /Save key/i });
    // openai is the second provider
    await user.click(saveKeyButtons[1]);

    await waitFor(() => {
      expect(credentialsUpdateMock).toHaveBeenCalledWith("openai", {
        api_key: "sk-openai-key",
        api_type: "chat",
      });
    });
  });

  it("renders Remove button when provider is configured", async () => {
    credentialsListMock.mockResolvedValue({
      data: [
        { provider: "openai", configured: true, masked_key: "sk-...abc" },
      ],
    });

    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByText("Remove")).toBeInTheDocument();
    });
  });

  it("shows masked key when provider is configured", async () => {
    credentialsListMock.mockResolvedValue({
      data: [
        { provider: "openai", configured: true, masked_key: "sk-...abc" },
      ],
    });

    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByText("Key: sk-...abc")).toBeInTheDocument();
    });
  });

  it("shows replace placeholder when provider is already configured", async () => {
    credentialsListMock.mockResolvedValue({
      data: [
        { provider: "openai", configured: true, masked_key: "sk-...abc" },
      ],
    });

    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByPlaceholderText("Replace existing key...")).toBeInTheDocument();
    });
  });

  it("opens remove confirmation dialog when Remove is clicked", async () => {
    const user = userEvent.setup();
    credentialsListMock.mockResolvedValue({
      data: [
        { provider: "openai", configured: true, masked_key: "sk-...abc" },
      ],
    });

    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByText("Remove")).toBeInTheDocument();
    });

    await user.click(screen.getByText("Remove"));

    await waitFor(() => {
      expect(screen.getByText("Remove API key")).toBeInTheDocument();
    });
    expect(screen.getByText(/Are you sure you want to remove the OpenAI API key/)).toBeInTheDocument();
  });

  it("calls credentials.delete when confirming removal", async () => {
    const user = userEvent.setup();
    credentialsListMock.mockResolvedValue({
      data: [
        { provider: "openai", configured: true, masked_key: "sk-...abc" },
      ],
    });

    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByText("Remove")).toBeInTheDocument();
    });

    await user.click(screen.getByText("Remove"));

    await waitFor(() => {
      expect(screen.getByText("Remove API key")).toBeInTheDocument();
    });

    // The dialog has a "Remove" action button
    const dialogRemoveBtn = screen.getByRole("button", { name: /^Remove$/ });
    await user.click(dialogRemoveBtn);

    await waitFor(() => {
      expect(credentialsDeleteMock).toHaveBeenCalledWith("openai");
    });
  });

  it("calls settings.update when save model is clicked", async () => {
    const user = userEvent.setup();
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Save model/i })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: /Save model/i }));

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalled();
    });
  });

  it("toggles password visibility when eye icon is clicked", async () => {
    const user = userEvent.setup();
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByPlaceholderText("sk-ant-...")).toBeInTheDocument();
    });

    const input = screen.getByPlaceholderText("sk-ant-...");
    expect(input).toHaveAttribute("type", "password");

    // Find the eye toggle button closest to this input
    const inputContainer = input.closest(".relative.flex-1")!;
    const toggleBtn = inputContainer.querySelector("button")!;
    await user.click(toggleBtn);

    expect(input).toHaveAttribute("type", "text");
  });
});
