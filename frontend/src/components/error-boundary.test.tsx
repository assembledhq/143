import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ErrorBoundary } from "./error-boundary";

function ThrowingChild({ shouldThrow }: { shouldThrow: boolean }) {
  if (shouldThrow) throw new Error("boom");
  return <div>All good</div>;
}

describe("ErrorBoundary", () => {
  // Suppress React error boundary console.error noise in test output
  const originalError = console.error;
  beforeEach(() => {
    console.error = vi.fn();
  });
  afterEach(() => {
    console.error = originalError;
  });

  it("renders children when no error", () => {
    render(
      <ErrorBoundary>
        <div>Hello</div>
      </ErrorBoundary>,
    );

    expect(screen.getByText("Hello")).toBeInTheDocument();
  });

  it("renders default fallback UI when a child throws", () => {
    render(
      <ErrorBoundary>
        <ThrowingChild shouldThrow />
      </ErrorBoundary>,
    );

    expect(screen.getByText("Something went wrong")).toBeInTheDocument();
    expect(screen.getByText(/unexpected error/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /try again/i })).toBeInTheDocument();
  });

  it("renders custom fallback when provided", () => {
    render(
      <ErrorBoundary fallback={<div>Custom error page</div>}>
        <ThrowingChild shouldThrow />
      </ErrorBoundary>,
    );

    expect(screen.getByText("Custom error page")).toBeInTheDocument();
    expect(screen.queryByText("Something went wrong")).not.toBeInTheDocument();
  });

  it("recovers when 'Try again' is clicked", async () => {
    const user = userEvent.setup();

    // We need a component that can switch between throwing and not
    let shouldThrow = true;
    function Toggler() {
      if (shouldThrow) throw new Error("boom");
      return <div>Recovered</div>;
    }

    const { rerender } = render(
      <ErrorBoundary>
        <Toggler />
      </ErrorBoundary>,
    );

    expect(screen.getByText("Something went wrong")).toBeInTheDocument();

    // Stop throwing, then click Try again
    shouldThrow = false;
    await user.click(screen.getByRole("button", { name: /try again/i }));

    // After resetting, ErrorBoundary re-renders children
    rerender(
      <ErrorBoundary>
        <Toggler />
      </ErrorBoundary>,
    );

    expect(screen.getByText("Recovered")).toBeInTheDocument();
  });
});
