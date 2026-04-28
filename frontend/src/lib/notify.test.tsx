import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { notify } from "./notify";
import type { ReactNode } from "react";

const { customMock } = vi.hoisted(() => ({
  customMock: vi.fn(),
}));

vi.mock("sonner", () => ({
  toast: {
    custom: customMock,
    dismiss: vi.fn(),
  },
}));

describe("notify", () => {
  beforeEach(() => {
    customMock.mockReset();
  });

  it("renders compact success toasts without dismiss controls by default", () => {
    notify.success("Organization created");

    expect(customMock).toHaveBeenCalledTimes(1);

    const [renderer, options] = customMock.mock.calls[0] as [(toastId: string | number) => ReactNode, { duration?: number }];
    expect(options).toMatchObject({ duration: 3200 });

    render(<>{renderer("toast-1")}</>);
    expect(screen.getByText("Organization created")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Dismiss notification" })).not.toBeInTheDocument();
  });

  it("renders dismissible error toasts with descriptions", () => {
    notify.error("PR creation failed", { description: "GitHub rejected the branch update." });

    expect(customMock).toHaveBeenCalledTimes(1);

    const [renderer, options] = customMock.mock.calls[0] as [(toastId: string | number) => ReactNode, { duration?: number }];
    expect(options).toMatchObject({ duration: 10000 });

    render(<>{renderer("toast-2")}</>);
    expect(screen.getByText("PR creation failed")).toBeInTheDocument();
    expect(screen.getByText("GitHub rejected the branch update.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Dismiss notification" })).toBeInTheDocument();
  });
});
