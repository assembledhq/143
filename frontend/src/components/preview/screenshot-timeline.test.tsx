import { describe, expect, it } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import { ScreenshotTimeline } from "./screenshot-timeline";
import type { PreviewSnapshot } from "@/lib/preview-types";

function makeSnapshot(overrides: Partial<PreviewSnapshot> = {}): PreviewSnapshot {
  return {
    id: "snap-1",
    instance_id: "inst-1",
    trigger: "baseline",
    screenshot_url: "/screenshots/snap-1.png",
    viewport_width: 1280,
    viewport_height: 720,
    console_error_count: 0,
    created_at: new Date().toISOString(),
    ...overrides,
  };
}

describe("ScreenshotTimeline", () => {
  it("returns null when there are no snapshots", () => {
    const { container } = renderWithProviders(
      <ScreenshotTimeline snapshots={[]} />
    );
    expect(container.firstChild).toBeNull();
  });

  it("shows the snapshot count in the header", () => {
    const snapshots = [
      makeSnapshot({ id: "s1" }),
      makeSnapshot({ id: "s2" }),
      makeSnapshot({ id: "s3" }),
    ];
    renderWithProviders(<ScreenshotTimeline snapshots={snapshots} />);
    expect(screen.getByText("Screenshots (3)")).toBeInTheDocument();
  });

  it("renders a thumbnail for each snapshot", () => {
    const snapshots = [
      makeSnapshot({ id: "s1", screenshot_url: "/img/s1.png" }),
      makeSnapshot({ id: "s2", screenshot_url: "/img/s2.png" }),
    ];
    renderWithProviders(<ScreenshotTimeline snapshots={snapshots} />);
    const images = screen.getAllByRole("img");
    expect(images).toHaveLength(2);
  });

  it("shows trigger badge labels", () => {
    const snapshots = [
      makeSnapshot({ id: "s1", trigger: "baseline" }),
      makeSnapshot({ id: "s2", trigger: "agent_change" }),
      makeSnapshot({ id: "s3", trigger: "hmr_update" }),
    ];
    renderWithProviders(<ScreenshotTimeline snapshots={snapshots} />);
    expect(screen.getByText("Baseline")).toBeInTheDocument();
    expect(screen.getByText("Agent Change")).toBeInTheDocument();
    expect(screen.getByText("HMR")).toBeInTheDocument();
  });

  it("renders scroll navigation buttons", () => {
    const snapshots = [makeSnapshot()];
    renderWithProviders(<ScreenshotTimeline snapshots={snapshots} />);
    const buttons = screen.getAllByRole("button");
    // Should have at least the left/right scroll buttons
    expect(buttons.length).toBeGreaterThanOrEqual(2);
  });
});
