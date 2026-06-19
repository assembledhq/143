import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, waitFor, userEvent, within } from "@/test/test-utils";
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
        default_llm_model: "gpt-5.4-mini",
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
      openai: ["gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano"],
      anthropic: ["claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5"],
      gemini: ["gemini-3.1-pro", "gemini-3-flash", "gemini-2.5-pro", "gemini-2.5-flash"],
    },
  }),
  llmDefaultsMock: vi.fn().mockResolvedValue({
    data: { openai: "sk-...plat" },
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

async function openEditDialog(provider: "Anthropic" | "OpenAI" | "Gemini" | "OpenRouter") {
  const user = userEvent.setup();
  const row = (await screen.findByText(provider)).closest(
    "[data-testid='provider-key-row']",
  )!;
  const button = within(row as HTMLElement).getByRole("button", { name: /^(Edit|Add)$/ });
  await user.click(button);
  return { user };
}

describe("LLMPage", () => {
  beforeEach(() => {
    settingsGetMock.mockClear();
    credentialsListMock.mockClear();
    credentialsListMock.mockResolvedValue({ data: [] });
    credentialsUpdateMock.mockClear();
    credentialsUpdateMock.mockResolvedValue({});
    credentialsDeleteMock.mockClear();
    settingsUpdateMock.mockClear();
    llmModelsMock.mockClear();
    llmDefaultsMock.mockClear();
    llmDefaultsMock.mockResolvedValue({ data: { openai: "sk-...plat" } });
  });

  it("renders the page header", async () => {
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "App LLM", level: 1 })).toBeInTheDocument();
    });
    expect(
      screen.getByText(
        /Configure models for app-generated titles, PR descriptions, validation, prioritization, and project generation\. Coding-agent credentials are managed separately on Coding agents\./i,
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Coding agents" })).toHaveAttribute("href", "/settings/agent");
    expect(
      screen.queryByText(/These are separate from coding agents on the Agent page/i),
    ).not.toBeInTheDocument();
  });

  it("renders provider keys section", async () => {
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByText("Provider keys")).toBeInTheDocument();
    });
  });

  it("renders default model section", async () => {
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Default model" })).toBeInTheDocument();
    });
    expect(
      screen.getByText(/Choose the default model for app features like PR descriptions, titles, validation, and project generation/i),
    ).toBeInTheDocument();
  });

  it("shows the platform-LLM alert when no platform provider is configured", async () => {
    llmDefaultsMock.mockResolvedValueOnce({ data: {} });

    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByText(/Platform LLM not configured/i)).toBeInTheDocument();
    });
    expect(screen.getByRole("link", { name: /self-hosting guide/i })).toHaveAttribute(
      "href",
      expect.stringContaining("/docs/self-hosting/platform-llm.md"),
    );
  });

  it("hides the platform-LLM alert when a platform provider is configured", async () => {
    llmDefaultsMock.mockResolvedValueOnce({ data: { openai: "sk-...abc" } });

    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(llmDefaultsMock).toHaveBeenCalled();
    });
    await waitFor(() => {
      expect(screen.queryByText(/Platform LLM not configured/i)).not.toBeInTheDocument();
    });
  });

  it("renders configured status dot when credentials exist", async () => {
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
      // There should be at least one Configured dot once the list loads.
      expect(screen.getAllByLabelText("Configured").length).toBeGreaterThan(0);
    });
  });

  it("renders a row for each provider, including Gemini", async () => {
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByText("Anthropic")).toBeInTheDocument();
    });
    expect(screen.getByText("OpenAI")).toBeInTheDocument();
    expect(screen.getByText("Gemini")).toBeInTheDocument();
    expect(screen.getByText("OpenRouter")).toBeInTheDocument();
  });

  it("opens the Anthropic dialog with the sk-ant-... placeholder", async () => {
    renderWithProviders(<LLMPage />);
    await openEditDialog("Anthropic");

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: /Anthropic API key/i })).toBeInTheDocument();
    });
    expect(screen.getByPlaceholderText("sk-ant-...")).toBeInTheDocument();
  });

  it("opens the Gemini dialog with the AIza... placeholder", async () => {
    renderWithProviders(<LLMPage />);
    await openEditDialog("Gemini");

    await waitFor(() => {
      expect(screen.getByPlaceholderText("AIza...")).toBeInTheDocument();
    });
  });

  it("disables Save in the dialog until a key is typed", async () => {
    renderWithProviders(<LLMPage />);
    const { user } = await openEditDialog("Anthropic");

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: /Anthropic API key/i })).toBeInTheDocument();
    });
    const saveBtn = screen.getByRole("button", { name: "Save" });
    expect(saveBtn).toBeDisabled();

    await user.type(screen.getByPlaceholderText("sk-ant-..."), "sk-ant-test123");
    expect(saveBtn).toBeEnabled();
  });

  it("calls credentials.update with the typed key via the dialog", async () => {
    renderWithProviders(<LLMPage />);
    const { user } = await openEditDialog("Anthropic");

    await waitFor(() => {
      expect(screen.getByPlaceholderText("sk-ant-...")).toBeInTheDocument();
    });

    await user.type(screen.getByPlaceholderText("sk-ant-..."), "sk-ant-test123");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(credentialsUpdateMock).toHaveBeenCalledWith("anthropic", { api_key: "sk-ant-test123" });
    });
  });

  it("sends api_type for openai provider when saving via the dialog", async () => {
    renderWithProviders(<LLMPage />);
    const { user } = await openEditDialog("OpenAI");

    await waitFor(() => {
      expect(screen.getByPlaceholderText("sk-...")).toBeInTheDocument();
    });

    await user.type(screen.getByPlaceholderText("sk-..."), "sk-openai-key");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(credentialsUpdateMock).toHaveBeenCalledWith("openai", {
        api_key: "sk-openai-key",
        api_type: "chat",
      });
    });
  });

  it("renders the Remove button in the dialog when a provider is configured", async () => {
    credentialsListMock.mockResolvedValue({
      data: [{ provider: "openai", configured: true, masked_key: "sk-...abc" }],
    });

    renderWithProviders(<LLMPage />);
    await openEditDialog("OpenAI");

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Remove" })).toBeInTheDocument();
    });
  });

  it("shows the masked key in the dialog when a provider is configured", async () => {
    credentialsListMock.mockResolvedValue({
      data: [{ provider: "openai", configured: true, masked_key: "sk-...abc" }],
    });

    renderWithProviders(<LLMPage />);
    await openEditDialog("OpenAI");

    // The masked key appears both in the row and in the dialog.
    await waitFor(() => {
      expect(screen.getAllByText("sk-...abc").length).toBeGreaterThanOrEqual(1);
    });
    expect(screen.getByPlaceholderText("Replace existing key...")).toBeInTheDocument();
  });

  it("opens the remove confirmation dialog from the edit dialog", async () => {
    credentialsListMock.mockResolvedValue({
      data: [{ provider: "openai", configured: true, masked_key: "sk-...abc" }],
    });

    renderWithProviders(<LLMPage />);
    const { user } = await openEditDialog("OpenAI");

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Remove" })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Remove" }));

    await waitFor(() => {
      expect(screen.getByText("Remove API key")).toBeInTheDocument();
    });
    expect(screen.getByText(/Are you sure you want to remove the OpenAI API key/)).toBeInTheDocument();
  });

  it("calls credentials.delete when confirming removal", async () => {
    credentialsListMock.mockResolvedValue({
      data: [{ provider: "openai", configured: true, masked_key: "sk-...abc" }],
    });

    renderWithProviders(<LLMPage />);
    const { user } = await openEditDialog("OpenAI");

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Remove" })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Remove" }));

    // Scope the confirmation click to the AlertDialog so we don't accidentally
    // click the "Remove" button in the still-open ProviderKeyDialog.
    const confirmDialog = await screen.findByRole("alertdialog");
    await user.click(within(confirmDialog).getByRole("button", { name: "Remove" }));

    await waitFor(() => {
      expect(credentialsDeleteMock).toHaveBeenCalledWith("openai");
    });
  });

  it("toggles password visibility when the eye icon is clicked", async () => {
    renderWithProviders(<LLMPage />);
    const { user } = await openEditDialog("Anthropic");

    await waitFor(() => {
      expect(screen.getByPlaceholderText("sk-ant-...")).toBeInTheDocument();
    });

    const input = screen.getByPlaceholderText("sk-ant-...");
    expect(input).toHaveAttribute("type", "password");

    await user.click(screen.getByRole("button", { name: /show key/i }));
    expect(input).toHaveAttribute("type", "text");
  });

  it("autosaves the default model when a new option is selected", async () => {
    const user = userEvent.setup();
    renderWithProviders(<LLMPage />);

    const combobox = await screen.findByRole("combobox", { name: /LLM Model/i });
    await user.click(combobox);
    await user.click(await screen.findByRole("option", { name: "gpt-5.4-nano" }));

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledWith({
        settings: { llm_model: "gpt-5.4-nano" },
      });
    });
  });

  it("labels the key 'Using 143's default OpenAI key' when only the platform default is available", async () => {
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByText(/Using 143's default OpenAI key/i)).toBeInTheDocument();
    });
    // The cost-cap callout should appear without implying org keys unlock app-level runtime models.
    expect(screen.getByText(/capped at lower-cost models/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Add your own OpenAI key/i })).not.toBeInTheDocument();
  });

  it("filters the model dropdown to mini/nano when only the platform default is available", async () => {
    const user = userEvent.setup();
    renderWithProviders(<LLMPage />);

    const combobox = await screen.findByRole("combobox", { name: /LLM Model/i });
    await user.click(combobox);
    expect(await screen.findByRole("option", { name: "gpt-5.4-mini" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "gpt-5.4-nano" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "gpt-5.4" })).not.toBeInTheDocument();
  });

  it("keeps the model dropdown capped when the org has its own OpenAI key", async () => {
    credentialsListMock.mockResolvedValue({
      data: [
        { provider: "openai", configured: true, masked_key: "sk-...test" },
      ],
    });
    const user = userEvent.setup();
    renderWithProviders(<LLMPage />);

    const combobox = await screen.findByRole("combobox", { name: /LLM Model/i });
    await user.click(combobox);
    expect(await screen.findByRole("option", { name: "gpt-5.4-mini" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "gpt-5.4-nano" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "gpt-5.4" })).not.toBeInTheDocument();
  });

  it("keeps the owner caption on the platform OpenAI key even when the org has its own key", async () => {
    credentialsListMock.mockResolvedValue({
      data: [
        { provider: "openai", configured: true, masked_key: "sk-...test" },
      ],
    });
    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByText(/Using 143's default OpenAI key/i)).toBeInTheDocument();
    });
    expect(screen.queryByText(/Uses your OpenAI key/)).not.toBeInTheDocument();
  });

  it("shows an amber warning when no provider for the default model is configured", async () => {
    llmDefaultsMock.mockResolvedValueOnce({ data: {} });

    renderWithProviders(<LLMPage />);

    await waitFor(() => {
      expect(screen.getByText(/No provider key configured/)).toBeInTheDocument();
    });
  });

  it("closes the edit dialog after a successful save", async () => {
    renderWithProviders(<LLMPage />);
    const { user } = await openEditDialog("Anthropic");

    await waitFor(() => {
      expect(screen.getByPlaceholderText("sk-ant-...")).toBeInTheDocument();
    });

    await user.type(screen.getByPlaceholderText("sk-ant-..."), "sk-ant-test");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(screen.queryByRole("heading", { name: /Anthropic API key/i })).not.toBeInTheDocument();
    });
  });

  it("stays open when reopening a provider dialog shortly after a successful save", async () => {
    renderWithProviders(<LLMPage />);
    const first = await openEditDialog("Anthropic");

    await waitFor(() => {
      expect(screen.getByPlaceholderText("sk-ant-...")).toBeInTheDocument();
    });

    await first.user.type(screen.getByPlaceholderText("sk-ant-..."), "sk-ant-test");
    await first.user.click(screen.getByRole("button", { name: "Save" }));

    // Dialog closes after the save.
    await waitFor(() => {
      expect(screen.queryByRole("heading", { name: /Anthropic API key/i })).not.toBeInTheDocument();
    });

    // Reopen the same provider's dialog immediately — the lingering "success"
    // save status must not cause it to auto-close.
    await openEditDialog("Anthropic");

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: /Anthropic API key/i })).toBeInTheDocument();
    });

    // Confirm it's still open after a microtask — the auto-close effect would
    // have fired by now if it were going to.
    await Promise.resolve();
    expect(screen.getByRole("heading", { name: /Anthropic API key/i })).toBeInTheDocument();
  });

  it("surfaces the server error message when saving a key fails", async () => {
    credentialsUpdateMock.mockRejectedValueOnce(new Error("Invalid API key"));

    renderWithProviders(<LLMPage />);
    const { user } = await openEditDialog("Anthropic");

    await waitFor(() => {
      expect(screen.getByPlaceholderText("sk-ant-...")).toBeInTheDocument();
    });

    await user.type(screen.getByPlaceholderText("sk-ant-..."), "sk-ant-test");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(screen.getByText("Invalid API key")).toBeInTheDocument();
    });
  });

  it("surfaces an error dialog and re-enables the row when delete fails", async () => {
    credentialsListMock.mockResolvedValueOnce({
      data: [{ provider: "anthropic", configured: true, masked_key: "sk-ant-••••" }],
    });
    credentialsDeleteMock.mockRejectedValueOnce(new Error("Something broke"));

    renderWithProviders(<LLMPage />);
    const { user } = await openEditDialog("Anthropic");

    // Click the "Remove" ghost button inside the edit dialog.
    const editDialog = await screen.findByRole("dialog");
    await user.click(within(editDialog).getByRole("button", { name: "Remove" }));

    // Confirm in the AlertDialog that opens (scope by the dialog title).
    const confirmDialog = await screen.findByRole("alertdialog");
    await user.click(within(confirmDialog).getByRole("button", { name: "Remove" }));

    await waitFor(() => {
      expect(screen.getByText(/Couldn.?t remove API key/)).toBeInTheDocument();
    });
    expect(screen.getByText("Something broke")).toBeInTheDocument();
  });
});
