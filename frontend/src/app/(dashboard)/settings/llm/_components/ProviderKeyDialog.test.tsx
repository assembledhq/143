import { describe, expect, it, vi } from "vitest";
import { useState } from "react";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { ProviderKeyDialog } from "./ProviderKeyDialog";
import type { SaveStatus } from "./ProviderKeyDialog";

const info = {
  name: "OpenAI",
  description: "OpenAI models (GPT series)",
  keyPlaceholder: "sk-...",
};

interface HarnessProps {
  saveStatus?: SaveStatus;
  errorMessage?: string;
  existingMaskedKey?: string;
  onSave?: (key: string) => void;
  onRemove?: () => void;
  initialOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
}

function Harness({
  saveStatus = "idle",
  errorMessage,
  existingMaskedKey,
  onSave = () => {},
  onRemove,
  initialOpen = true,
  onOpenChange,
}: HarnessProps) {
  const [open, setOpen] = useState(initialOpen);
  return (
    <ProviderKeyDialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        onOpenChange?.(next);
      }}
      provider="openai"
      info={info}
      existingMaskedKey={existingMaskedKey}
      saveStatus={saveStatus}
      errorMessage={errorMessage}
      onSave={onSave}
      onRemove={onRemove}
    />
  );
}

describe("ProviderKeyDialog", () => {
  it("shows the provider name as the dialog title", () => {
    renderWithProviders(<Harness />);
    expect(screen.getByRole("heading", { name: /OpenAI API key/i })).toBeInTheDocument();
  });

  it("renders the provider description as the dialog body", () => {
    renderWithProviders(<Harness />);
    expect(screen.getByText("OpenAI models (GPT series)")).toBeInTheDocument();
  });

  it("disables Save until a key is typed", async () => {
    const user = userEvent.setup();
    renderWithProviders(<Harness />);
    const saveBtn = screen.getByRole("button", { name: "Save" });
    expect(saveBtn).toBeDisabled();
    await user.type(screen.getByPlaceholderText("sk-..."), "sk-new");
    expect(saveBtn).toBeEnabled();
  });

  it("calls onSave with the trimmed key", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    renderWithProviders(<Harness onSave={onSave} />);
    await user.type(screen.getByPlaceholderText("sk-..."), "  sk-trim  ");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(onSave).toHaveBeenCalledWith("sk-trim");
  });

  it("shows the existing masked key when configured", () => {
    renderWithProviders(<Harness existingMaskedKey="sk-...e14c" />);
    expect(screen.getByText(/Current key:/)).toBeInTheDocument();
    expect(screen.getByText("sk-...e14c")).toBeInTheDocument();
  });

  it("renders the Remove button only when onRemove is provided", () => {
    const { rerender } = renderWithProviders(<Harness onRemove={() => {}} />);
    expect(screen.getByRole("button", { name: "Remove" })).toBeInTheDocument();
    rerender(<Harness />);
    expect(screen.queryByRole("button", { name: "Remove" })).not.toBeInTheDocument();
  });

  it("fires onRemove when Remove is clicked", async () => {
    const user = userEvent.setup();
    const onRemove = vi.fn();
    renderWithProviders(<Harness onRemove={onRemove} />);
    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(onRemove).toHaveBeenCalledTimes(1);
  });

  it("shows the error message when saveStatus is error", () => {
    renderWithProviders(<Harness saveStatus="error" errorMessage="Invalid key" />);
    expect(screen.getByText("Invalid key")).toBeInTheDocument();
  });

  it("closes the dialog when saveStatus flips to success", async () => {
    const onOpenChange = vi.fn();

    const { rerender } = renderWithProviders(
      <ProviderKeyDialog
        open
        onOpenChange={onOpenChange}
        provider="openai"
        info={info}
        saveStatus="saving"
        onSave={() => {}}
      />,
    );
    // Flip status prop to "success" — the dialog should request to close.
    rerender(
      <ProviderKeyDialog
        open
        onOpenChange={onOpenChange}
        provider="openai"
        info={info}
        saveStatus="success"
        onSave={() => {}}
      />,
    );
    await waitFor(() => {
      expect(onOpenChange).toHaveBeenCalledWith(false);
    });
  });
});
