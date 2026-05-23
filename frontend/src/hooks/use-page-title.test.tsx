import { render, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { usePageTitle } from "./use-page-title";

function TitleProbe({ title, fallback }: { title: string | null; fallback?: string }) {
  usePageTitle(title, fallback);
  return null;
}

describe("usePageTitle", () => {
  afterEach(() => {
    document.title = "";
  });

  it("updates the browser title with a page-specific value", async () => {
    render(<TitleProbe title="Fixed TypeError by adding null check" />);

    await waitFor(() => {
      expect(document.title).toBe("143 | Fixed TypeError by adding null check");
    });
  });

  it("does not replace the current title while dynamic data is still missing", async () => {
    document.title = "143 | Session";

    render(<TitleProbe title={null} />);

    await waitFor(() => {
      expect(document.title).toBe("143 | Session");
    });
  });

  it("uses a fallback title while dynamic data is still missing", async () => {
    render(<TitleProbe title={null} fallback="Project" />);

    await waitFor(() => {
      expect(document.title).toBe("143 | Project");
    });
  });

  it("replaces the fallback when the dynamic title arrives", async () => {
    const { rerender } = render(<TitleProbe title={null} fallback="Project" />);

    await waitFor(() => {
      expect(document.title).toBe("143 | Project");
    });

    rerender(<TitleProbe title="Stabilize preview startup" fallback="Project" />);

    await waitFor(() => {
      expect(document.title).toBe("143 | Stabilize preview startup");
    });
  });
});
