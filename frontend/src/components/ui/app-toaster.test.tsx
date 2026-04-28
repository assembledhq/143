import { describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
import { AppToaster } from "./app-toaster";

const { toasterMock } = vi.hoisted(() => ({
  toasterMock: vi.fn(() => <div data-testid="sonner-toaster" />),
}));

vi.mock("sonner", () => ({
  Toaster: toasterMock,
}));

describe("AppToaster", () => {
  it("passes the shared toaster positioning to sonner without a global close button", () => {
    render(<AppToaster />);

    expect(toasterMock).toHaveBeenCalledTimes(1);

    const [props] = toasterMock.mock.calls[0] as unknown as [{ position: string; expand: boolean; closeButton: boolean; toastOptions: { unstyled: boolean; classNames: Record<string, string> } }];
    expect(props.position).toBe("bottom-right");
    expect(props.expand).toBe(false);
    expect(props.closeButton).toBe(false);
    expect(props.toastOptions.unstyled).toBe(true);
    expect(props.toastOptions.classNames.toast).toContain("bg-transparent");
  });
});
