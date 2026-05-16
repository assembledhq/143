import { renderToString } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { useInView } from "./use-in-view";

function HookProbe() {
  const { inView } = useInView();
  return <div data-in-view={String(inView)} />;
}

describe("useInView", () => {
  it("renders visible content on the server before hydration", () => {
    const originalWindow = globalThis.window;

    // Match the hook's server-render branch even though the test suite runs in
    // jsdom by default.
    Reflect.deleteProperty(globalThis, "window");
    const html = renderToString(<HookProbe />);
    globalThis.window = originalWindow;

    expect(html).toContain('data-in-view="true"');
  });
});
