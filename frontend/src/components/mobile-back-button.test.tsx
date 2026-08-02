import type { AnchorHTMLAttributes, ReactNode } from "react";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MobileBackButton } from "./mobile-back-button";

vi.mock("next/link", () => ({
  default: ({
    children,
    href,
    onClick,
    ...props
  }: AnchorHTMLAttributes<HTMLAnchorElement> & {
    children: ReactNode;
    href: string;
  }) => (
    <a
      href={href}
      onClick={(event) => {
        onClick?.(event);
        event.preventDefault();
      }}
      {...props}
    >
      {children}
    </a>
  ),
}));

vi.mock("next/navigation", () => ({
  useSearchParams: () => new URLSearchParams("repo=assembledhq%2F143"),
}));

describe("MobileBackButton", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("acknowledges a route transition immediately and preserves list state", () => {
    render(
      <MobileBackButton to="/sessions" label="Back to sessions" />,
    );

    const link = screen.getByRole("link", { name: "Back to sessions" });
    expect(link).toHaveAttribute(
      "href",
      "/sessions?repo=assembledhq%2F143",
    );
    expect(link).toHaveClass("size-11", "sm:size-11", "md:hidden");

    fireEvent.click(link);

    expect(
      screen.getByRole("link", { name: "Back to sessions, loading" }),
    ).toHaveAttribute("aria-busy", "true");
  });

  it("releases the pending state when a slow transition cannot finish", () => {
    render(
      <MobileBackButton to="/sessions" label="Back to sessions" />,
    );

    fireEvent.click(screen.getByRole("link", { name: "Back to sessions" }));
    act(() => {
      vi.advanceTimersByTime(8_000);
    });

    expect(
      screen.getByRole("link", { name: "Back to sessions" }),
    ).not.toHaveAttribute("aria-busy");
  });
});
