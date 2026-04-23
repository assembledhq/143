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
  it("passes the shared toast styling and positioning to sonner", () => {
    render(<AppToaster />);

    expect(toasterMock).toHaveBeenCalledTimes(1);

    const [props] = toasterMock.mock.calls[0] as unknown as [{ position: string; expand: boolean; closeButton: boolean; toastOptions: { unstyled: boolean; classNames: Record<string, string> } }];
    expect(props.position).toBe("bottom-right");
    expect(props.expand).toBe(true);
    expect(props.closeButton).toBe(true);
    expect(props.toastOptions.unstyled).toBe(true);
    expect(props.toastOptions.classNames.toast).toContain("rounded-xl");
    expect(props.toastOptions.classNames.error).toContain("border-destructive/25");
  });
});
