import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { renderToString } from "react-dom/server";
import { usePersistedPanelWidth } from "./use-persisted-panel-width";

function TestPanelWidth() {
  const { width, resizeBy } = usePersistedPanelWidth({
    storageKey: "143:test-panel-width",
    defaultWidth: 236,
    minWidth: 200,
    maxWidth: 300,
  });

  return (
    <>
      <div data-testid="panel" style={{ width: `${width}px` }} />
      <button type="button" onClick={() => resizeBy(80)}>
        grow
      </button>
    </>
  );
}

describe("usePersistedPanelWidth", () => {
  beforeEach(() => {
    window.localStorage.clear();
    vi.restoreAllMocks();
  });

  it("renders the default width during SSR even when storage has a saved width", () => {
    window.localStorage.setItem("143:test-panel-width", "280");

    const markup = renderToString(<TestPanelWidth />);

    expect(markup).toContain("width:236px");
  });

  it("follows storage updates after a local resize", async () => {
    render(<TestPanelWidth />);

    fireEvent.click(screen.getByRole("button", { name: "grow" }));
    expect(screen.getByTestId("panel")).toHaveStyle({ width: "300px" });

    window.localStorage.setItem("143:test-panel-width", "240");
    act(() => {
      window.dispatchEvent(new Event("storage"));
    });

    await waitFor(() => {
      expect(screen.getByTestId("panel")).toHaveStyle({ width: "240px" });
    });
  });

  it("logs and falls back to the default width when localStorage read throws", () => {
    const error = new Error("storage blocked");
    const getItemSpy = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw error;
    });
    const consoleErrorSpy = vi.spyOn(console, "error").mockImplementation(() => {});

    render(<TestPanelWidth />);

    expect(screen.getByTestId("panel")).toHaveStyle({ width: "236px" });
    expect(consoleErrorSpy).toHaveBeenCalledWith(
      "failed to read persisted panel width",
      expect.objectContaining({ storageKey: "143:test-panel-width", error }),
    );

    getItemSpy.mockRestore();
  });

  it("logs localStorage write failures during resize", async () => {
    const error = new Error("storage blocked");
    const setItemSpy = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw error;
    });
    const consoleErrorSpy = vi.spyOn(console, "error").mockImplementation(() => {});

    render(<TestPanelWidth />);
    fireEvent.click(screen.getByRole("button", { name: "grow" }));

    await waitFor(() => {
      expect(consoleErrorSpy).toHaveBeenCalledWith(
        "failed to persist panel width",
        expect.objectContaining({ storageKey: "143:test-panel-width", width: 300, error }),
      );
    });

    setItemSpy.mockRestore();
  });
});
