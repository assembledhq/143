import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { mapSharedBrowserPoint, SharedBrowserSurface } from "./shared-browser-surface";

const mocks = vi.hoisted(() => ({
  control: vi.fn(),
  observe: vi.fn(),
  acquire: vi.fn(),
  release: vi.fn(),
  act: vi.fn(),
}));

vi.mock("@/lib/api", () => ({
  api: { sessions: { preview: {
    browserControl: mocks.control,
    observeBrowser: mocks.observe,
    acquireBrowserControl: mocks.acquire,
    returnBrowserControl: mocks.release,
    actAsHuman: mocks.act,
  } } },
}));

describe("SharedBrowserSurface", () => {
  beforeEach(() => {
    Object.values(mocks).forEach((mock) => mock.mockReset());
    mocks.observe.mockResolvedValue({
      title: "Dashboard",
      url: "https://preview.test/dashboard",
      viewport: { width: 1000, height: 500 },
      screenshot: { png_base64: "aW1hZ2U=", url: "https://preview.test/dashboard", page_title: "Dashboard", viewport: { width: 1000, height: 500 }, captured_at: new Date().toISOString() },
      console_cursor: 0,
      ready: true,
    });
    mocks.acquire.mockResolvedValue({ state: "human_control" });
    mocks.release.mockResolvedValue({ state: "agent_control" });
    mocks.act.mockResolvedValue({});
  });

  it("shows the agent-owned shared browser as watch-only", async () => {
    mocks.control.mockResolvedValue({ state: "agent_control" });
    renderWithProviders(<SharedBrowserSurface sessionId="session-1" />);
    expect(await screen.findByText("agent control")).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "Dashboard" })).toHaveAttribute("src", "data:image/png;base64,aW1hZ2U=");
    expect(screen.queryByRole("button", { name: "Interact with session browser" })).not.toBeInTheDocument();
  });

  it("acquires human control before exposing shared input", async () => {
    mocks.control.mockResolvedValue({ state: "agent_control" });
    const user = userEvent.setup();
    renderWithProviders(<SharedBrowserSurface sessionId="session-1" />);
    await user.click(await screen.findByRole("button", { name: /Take control/ }));
    expect(mocks.acquire).toHaveBeenCalledWith("session-1");
  });

  it("sends coordinate input only while the human lease is active", async () => {
    mocks.control.mockResolvedValue({ state: "human_control", lease_owner_id: "user-1", is_lease_owner: true });
    const user = userEvent.setup();
    renderWithProviders(<SharedBrowserSurface sessionId="session-1" />);
    const surface = await screen.findByRole("button", { name: "Interact with session browser" });
    vi.spyOn(surface, "getBoundingClientRect").mockReturnValue({ x: 0, y: 0, left: 0, top: 0, right: 500, bottom: 250, width: 500, height: 250, toJSON: () => ({}) });
    await user.pointer({ target: surface, coords: { clientX: 250, clientY: 125 }, keys: "[MouseLeft]" });
    await waitFor(() => expect(mocks.act).toHaveBeenCalledWith("session-1", [{ action: "click", x: 500, y: 250 }]));
  });

  it("queues rapid keyboard input in order without dropping keys", async () => {
    mocks.control.mockResolvedValue({ state: "human_control", lease_owner_id: "user-1", is_lease_owner: true });
    let resolveFirst!: (value: object) => void;
    mocks.act
      .mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve; }))
      .mockResolvedValue({});
    const user = userEvent.setup();
    renderWithProviders(<SharedBrowserSurface sessionId="session-1" />);
    const surface = await screen.findByRole("button", { name: "Interact with session browser" });

    surface.focus();
    await user.keyboard("abc");
    await waitFor(() => expect(mocks.act).toHaveBeenCalledTimes(1));
    expect(mocks.act).toHaveBeenNthCalledWith(1, "session-1", [{ action: "press", value: "a" }]);

    resolveFirst({});
    await waitFor(() => expect(mocks.act).toHaveBeenCalledTimes(3));
    expect(mocks.act).toHaveBeenNthCalledWith(2, "session-1", [{ action: "press", value: "b" }]);
    expect(mocks.act).toHaveBeenNthCalledWith(3, "session-1", [{ action: "press", value: "c" }]);
  });
});

describe("mapSharedBrowserPoint", () => {
  it("ignores letterbox clicks and maps the rendered viewport", () => {
    const rect = { left: 0, top: 0, width: 1000, height: 500 };
    expect(mapSharedBrowserPoint(rect, { width: 390, height: 844 }, 100, 250)).toBeNull();
    expect(mapSharedBrowserPoint(rect, { width: 390, height: 844 }, 500, 250)).toEqual({ x: 195, y: 422 });
  });
});
