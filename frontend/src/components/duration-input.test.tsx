import { describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
import { screen, userEvent } from "@/test/test-utils";
import { DurationInput } from "./duration-input";

describe("DurationInput", () => {
  it("defaults whole-minute values to minutes", () => {
    const onChangeSeconds = vi.fn();

    render(<DurationInput label="Timeout" valueSeconds={1800} onChangeSeconds={onChangeSeconds} />);

    expect(screen.getByLabelText("Timeout value")).toHaveValue(30);
    expect(screen.getByRole("combobox", { name: "Timeout unit" })).toHaveTextContent("Minutes");
  });

  it("commits seconds when the unit suffix changes", async () => {
    const user = userEvent.setup();
    const onChangeSeconds = vi.fn();

    render(<DurationInput label="Timeout" valueSeconds={30 * 60} onChangeSeconds={onChangeSeconds} />);

    await user.click(screen.getByRole("combobox", { name: "Timeout unit" }));
    await user.click(await screen.findByRole("option", { name: "Hours" }));

    expect(onChangeSeconds).toHaveBeenLastCalledWith(30 * 60 * 60);
  });

  it("clamps the displayed amount when switching to a smaller unit", async () => {
    const user = userEvent.setup();
    const onChangeSeconds = vi.fn();

    render(
      <DurationInput
        label="Timeout"
        valueSeconds={30 * 60}
        minSeconds={60}
        onChangeSeconds={onChangeSeconds}
      />,
    );

    await user.click(screen.getByRole("combobox", { name: "Timeout unit" }));
    await user.click(await screen.findByRole("option", { name: "Seconds" }));

    expect(onChangeSeconds).toHaveBeenLastCalledWith(60);
    expect(screen.getByLabelText("Timeout value")).toHaveValue(60);
  });

  it("clamps values on blur", async () => {
    const user = userEvent.setup();
    const onChangeSeconds = vi.fn();

    render(
      <DurationInput
        label="Timeout"
        valueSeconds={5 * 60}
        minSeconds={60}
        onChangeSeconds={onChangeSeconds}
      />,
    );

    const input = screen.getByLabelText("Timeout value");
    await user.clear(input);
    await user.type(input, "0");
    await user.tab();

    expect(onChangeSeconds).toHaveBeenLastCalledWith(60);
    expect(input).toHaveValue(1);
  });
});
