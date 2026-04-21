import { describe, expect, it, vi } from "vitest";
import { useState } from "react";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { PasswordField } from "./PasswordField";

function Controlled({ onChange }: { onChange?: (v: string) => void }) {
  const [v, setV] = useState("");
  return (
    <PasswordField
      value={v}
      onChange={(next) => {
        setV(next);
        onChange?.(next);
      }}
      placeholder="sk-..."
      ariaLabel="API key"
    />
  );
}

describe("PasswordField", () => {
  it("renders as a password input by default", () => {
    renderWithProviders(<Controlled />);
    expect(screen.getByPlaceholderText("sk-...")).toHaveAttribute("type", "password");
  });

  it("toggles to text when the eye icon is clicked", async () => {
    const user = userEvent.setup();
    renderWithProviders(<Controlled />);

    const input = screen.getByPlaceholderText("sk-...");
    expect(input).toHaveAttribute("type", "password");

    await user.click(screen.getByRole("button", { name: /show key/i }));
    expect(input).toHaveAttribute("type", "text");

    await user.click(screen.getByRole("button", { name: /hide key/i }));
    expect(input).toHaveAttribute("type", "password");
  });

  it("fires onChange with the typed value", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    renderWithProviders(<Controlled onChange={onChange} />);

    await user.type(screen.getByPlaceholderText("sk-..."), "abc");
    expect(onChange).toHaveBeenLastCalledWith("abc");
  });

  it("applies the aria-label on the input", () => {
    renderWithProviders(<Controlled />);
    expect(screen.getByLabelText("API key")).toBeInTheDocument();
  });

  it("honors disabled prop", () => {
    renderWithProviders(
      <PasswordField value="" onChange={() => {}} placeholder="sk-..." disabled />,
    );
    expect(screen.getByPlaceholderText("sk-...")).toBeDisabled();
  });

  it("auto-focuses when autoFocus is true", () => {
    renderWithProviders(
      <PasswordField value="" onChange={() => {}} placeholder="sk-..." autoFocus />,
    );
    expect(screen.getByPlaceholderText("sk-...")).toHaveFocus();
  });
});
