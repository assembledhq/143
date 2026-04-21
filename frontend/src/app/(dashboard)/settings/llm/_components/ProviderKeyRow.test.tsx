import { describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { ProviderKeyRow } from "./ProviderKeyRow";

const info = {
  name: "OpenAI",
  description: "OpenAI models (GPT series)",
  keyPlaceholder: "sk-...",
};

describe("ProviderKeyRow", () => {
  it("shows a grey not-configured dot and 'Add' button when not configured", () => {
    renderWithProviders(
      <ProviderKeyRow
        provider="openai"
        info={info}
        status={{ orgConfigured: false, platformAvailable: false }}
        isDefaultOwner={false}
        onEdit={() => {}}
      />,
    );
    expect(screen.getByLabelText("Not configured")).toBeInTheDocument();
    expect(screen.getByText("Not set")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add" })).toBeInTheDocument();
  });

  it("shows a green configured dot, masked key, and 'Edit' when configured", () => {
    renderWithProviders(
      <ProviderKeyRow
        provider="openai"
        info={info}
        status={{ orgConfigured: true, platformAvailable: false, maskedKey: "sk-...e14c" }}
        isDefaultOwner={false}
        onEdit={() => {}}
      />,
    );
    expect(screen.getByLabelText("Configured")).toBeInTheDocument();
    expect(screen.getByText("sk-...e14c")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit" })).toBeInTheDocument();
  });

  it("renders the default pill only when isDefaultOwner is true", () => {
    const { rerender } = renderWithProviders(
      <ProviderKeyRow
        provider="openai"
        info={info}
        status={{ orgConfigured: true, platformAvailable: false, maskedKey: "sk-...e14c" }}
        isDefaultOwner
        onEdit={() => {}}
      />,
    );
    expect(screen.getByText("default")).toBeInTheDocument();

    rerender(
      <ProviderKeyRow
        provider="openai"
        info={info}
        status={{ orgConfigured: true, platformAvailable: false, maskedKey: "sk-...e14c" }}
        isDefaultOwner={false}
        onEdit={() => {}}
      />,
    );
    expect(screen.queryByText("default")).not.toBeInTheDocument();
  });

  it("calls onEdit when the Edit button is clicked", async () => {
    const user = userEvent.setup();
    const onEdit = vi.fn();
    renderWithProviders(
      <ProviderKeyRow
        provider="openai"
        info={info}
        status={{ orgConfigured: true, platformAvailable: false, maskedKey: "sk-...e14c" }}
        isDefaultOwner={false}
        onEdit={onEdit}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Edit" }));
    expect(onEdit).toHaveBeenCalledTimes(1);
  });

  it("calls onEdit when the Add button is clicked", async () => {
    const user = userEvent.setup();
    const onEdit = vi.fn();
    renderWithProviders(
      <ProviderKeyRow
        provider="openai"
        info={info}
        status={{ orgConfigured: false, platformAvailable: false }}
        isDefaultOwner={false}
        onEdit={onEdit}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Add" }));
    expect(onEdit).toHaveBeenCalledTimes(1);
  });
});
