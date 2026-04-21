import { describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { DefaultModelCard } from "./DefaultModelCard";

const groups = [
  { label: "OpenAI", models: ["gpt-4o", "gpt-5.4-mini"] as readonly string[] },
];

describe("DefaultModelCard", () => {
  it("renders the owner-key caption with a check when owner is configured", () => {
    renderWithProviders(
      <DefaultModelCard
        value="gpt-5.4-mini"
        reasoningEffort=""
        modelGroups={groups}
        ownerProvider="openai"
        ownerProviderInfo={{ name: "OpenAI" }}
        ownerConfigured
        saving={false}
        saveStatus="idle"
        onChange={() => {}}
        onReasoningChange={() => {}}
        onSave={() => {}}
      />,
    );
    expect(screen.getByText(/Uses your OpenAI key/)).toBeInTheDocument();
  });

  it("shows an amber warning when the owner is not configured", () => {
    renderWithProviders(
      <DefaultModelCard
        value="gpt-5.4-mini"
        reasoningEffort=""
        modelGroups={groups}
        ownerProvider="openai"
        ownerProviderInfo={{ name: "OpenAI" }}
        ownerConfigured={false}
        saving={false}
        saveStatus="idle"
        onChange={() => {}}
        onReasoningChange={() => {}}
        onSave={() => {}}
      />,
    );
    expect(screen.getByText(/No provider key configured/)).toBeInTheDocument();
  });

  it("disables the Save button when the owner is not configured", () => {
    renderWithProviders(
      <DefaultModelCard
        value="gpt-5.4-mini"
        reasoningEffort=""
        modelGroups={groups}
        ownerProvider={null}
        ownerConfigured={false}
        saving={false}
        saveStatus="idle"
        onChange={() => {}}
        onReasoningChange={() => {}}
        onSave={() => {}}
      />,
    );
    expect(screen.getByRole("button", { name: "Save default model" })).toBeDisabled();
  });

  it("invokes onSave when the Save button is clicked", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    renderWithProviders(
      <DefaultModelCard
        value="gpt-5.4-mini"
        reasoningEffort=""
        modelGroups={groups}
        ownerProvider="openai"
        ownerProviderInfo={{ name: "OpenAI" }}
        ownerConfigured
        saving={false}
        saveStatus="idle"
        onChange={() => {}}
        onReasoningChange={() => {}}
        onSave={onSave}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Save default model" }));
    expect(onSave).toHaveBeenCalledTimes(1);
  });

  it("disables the model select when there are no model groups", () => {
    renderWithProviders(
      <DefaultModelCard
        value="gpt-5.4-mini"
        reasoningEffort=""
        modelGroups={[]}
        ownerProvider={null}
        ownerConfigured={false}
        saving={false}
        saveStatus="idle"
        onChange={() => {}}
        onReasoningChange={() => {}}
        onSave={() => {}}
      />,
    );
    expect(screen.getByRole("combobox", { name: /LLM Model/i })).toBeDisabled();
  });
});
