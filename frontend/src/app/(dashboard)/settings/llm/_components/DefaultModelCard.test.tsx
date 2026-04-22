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
        onChange={() => {}}
        onReasoningChange={() => {}}
      />,
    );
    expect(screen.getByText(/Uses your OpenAI key/)).toBeInTheDocument();
    expect(
      screen.getByText(/Used for organization-level LLM features, separate from the coding agents configured on the Agent settings page/i),
    ).toBeInTheDocument();
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
        onChange={() => {}}
        onReasoningChange={() => {}}
      />,
    );
    expect(screen.getByText(/No provider key configured/)).toBeInTheDocument();
  });

  it("fires onChange when a new model is selected", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    renderWithProviders(
      <DefaultModelCard
        value="gpt-5.4-mini"
        reasoningEffort=""
        modelGroups={groups}
        ownerProvider="openai"
        ownerProviderInfo={{ name: "OpenAI" }}
        ownerConfigured
        onChange={onChange}
        onReasoningChange={() => {}}
      />,
    );
    await user.click(screen.getByRole("combobox", { name: /LLM Model/i }));
    await user.click(await screen.findByRole("option", { name: "gpt-4o" }));
    expect(onChange).toHaveBeenCalledWith("gpt-4o");
  });

  it("disables the model select when there are no model groups", () => {
    renderWithProviders(
      <DefaultModelCard
        value="gpt-5.4-mini"
        reasoningEffort=""
        modelGroups={[]}
        ownerProvider={null}
        ownerConfigured={false}
        onChange={() => {}}
        onReasoningChange={() => {}}
      />,
    );
    expect(screen.getByRole("combobox", { name: /LLM Model/i })).toBeDisabled();
  });
});
