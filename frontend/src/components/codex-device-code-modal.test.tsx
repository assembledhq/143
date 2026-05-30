import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "@/test/mocks/server";
import { api } from "@/lib/api";
import { CodexDeviceCodeModal } from "./codex-device-code-modal";

const INITIATE_URL = "/api/v1/settings/codex-auth/initiate";
const STATUS_URL = "/api/v1/settings/codex-auth/status";

const mockDeviceAuth = {
  user_code: "ABCD-1234",
  verification_uri: "https://auth.example.com/device",
  expires_in: 600,
};

function setMobileMatch(matches: boolean) {
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: query === "(max-width: 639px)" ? matches : false,
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  });
}

describe("CodexDeviceCodeModal", () => {
  it("uses the shared mobile sheet layout", async () => {
    setMobileMatch(true);
    server.use(
      http.post(INITIATE_URL, () => {
        return HttpResponse.json({ data: mockDeviceAuth });
      }),
      http.get(STATUS_URL, () => {
        return HttpResponse.json({ data: { status: "pending" } });
      }),
    );

    render(<CodexDeviceCodeModal onClose={vi.fn()} />);

    const dialog = await screen.findByRole("dialog", { name: "Connect your ChatGPT account" });
    expect(dialog).toHaveAttribute("data-slot", "sheet-content");
    expect(dialog).toHaveClass("max-h-[100svh]", "overflow-hidden");
  });

  it("shows initiating state then device code on success", async () => {
    server.use(
      http.post(INITIATE_URL, () => {
        return HttpResponse.json({ data: mockDeviceAuth });
      }),
      http.get(STATUS_URL, () => {
        return HttpResponse.json({ data: { status: "pending" } });
      }),
    );

    render(<CodexDeviceCodeModal onClose={vi.fn()} />);

    expect(screen.getByText(/starting authentication/i)).toBeInTheDocument();

    await waitFor(() => {
      expect(screen.getByText("ABCD-1234")).toBeInTheDocument();
    });

    expect(screen.getByText("https://auth.example.com/device")).toBeInTheDocument();
    expect(screen.getByText(/waiting for authentication/i)).toBeInTheDocument();
  });

  it("shows error state when initiation fails", async () => {
    server.use(
      http.post(INITIATE_URL, () => {
        return HttpResponse.json({ error: { code: "FAIL", message: "Nope" } }, { status: 500 });
      }),
    );

    render(<CodexDeviceCodeModal onClose={vi.fn()} />);

    await waitFor(() => {
      expect(screen.getByText("Nope")).toBeInTheDocument();
    });
  });

  it("falls back to a generic error message when the thrown error has no message", async () => {
    // Simulate the edge case where the API layer throws an Error with an empty
    // message (e.g. the server response had no body and no status text). The
    // modal should render its generic fallback instead of an empty string.
    const spy = vi.spyOn(api.codexAuth, "initiate").mockRejectedValueOnce(new Error(""));

    render(<CodexDeviceCodeModal onClose={vi.fn()} />);

    await waitFor(() => {
      expect(screen.getByText(/failed to start authentication/i)).toBeInTheDocument();
    });

    spy.mockRestore();
  });

  it("calls onClose when Cancel is clicked", async () => {
    const onClose = vi.fn();

    server.use(
      http.post(INITIATE_URL, () => {
        return HttpResponse.json({ data: mockDeviceAuth });
      }),
      http.get(STATUS_URL, () => {
        return HttpResponse.json({ data: { status: "pending" } });
      }),
    );

    render(<CodexDeviceCodeModal onClose={onClose} />);

    await waitFor(() => {
      expect(screen.getByText("ABCD-1234")).toBeInTheDocument();
    });

    await userEvent.click(screen.getByRole("button", { name: /cancel/i }));
    expect(onClose).toHaveBeenCalled();
  });

  it("shows success state when auth completes", async () => {
    server.use(
      http.post(INITIATE_URL, () => {
        return HttpResponse.json({ data: mockDeviceAuth });
      }),
      http.get(STATUS_URL, () => {
        return HttpResponse.json({ data: { status: "completed" } });
      }),
    );

    render(<CodexDeviceCodeModal onClose={vi.fn()} />);

    await waitFor(() => {
      expect(screen.getByText("ABCD-1234")).toBeInTheDocument();
    });

    // Wait for the real 3s polling interval to fire and resolve
    await waitFor(() => {
      expect(screen.getByText(/connected successfully/i)).toBeInTheDocument();
    }, { timeout: 5000 });
  });

  it("shows error state when polling reports an auth error", async () => {
    server.use(
      http.post(INITIATE_URL, () => {
        return HttpResponse.json({ data: mockDeviceAuth });
      }),
      http.get(STATUS_URL, () => {
        return HttpResponse.json({ data: { status: "error", message: "authentication denied by user" } });
      }),
    );

    render(<CodexDeviceCodeModal onClose={vi.fn()} />);

    await waitFor(() => {
      expect(screen.getByText("ABCD-1234")).toBeInTheDocument();
    });

    // Wait for the polling interval to fire and resolve.
    await waitFor(() => {
      expect(screen.getByText(/authentication denied by user/i)).toBeInTheDocument();
    }, { timeout: 5000 });

    expect(screen.getByRole("button", { name: /try again/i })).toBeInTheDocument();
  });

  it("shows expired state and Try again button", async () => {
    server.use(
      http.post(INITIATE_URL, () => {
        return HttpResponse.json({ data: mockDeviceAuth });
      }),
      http.get(STATUS_URL, () => {
        return HttpResponse.json({ data: { status: "expired" } });
      }),
    );

    render(<CodexDeviceCodeModal onClose={vi.fn()} />);

    await waitFor(() => {
      expect(screen.getByText("ABCD-1234")).toBeInTheDocument();
    });

    // Wait for the real 3s polling interval to fire and resolve
    await waitFor(() => {
      expect(screen.getByText(/code expired/i)).toBeInTheDocument();
    }, { timeout: 5000 });

    expect(screen.getByRole("button", { name: /try again/i })).toBeInTheDocument();
  });

  it("shows 'Copied' feedback after clicking Copy", async () => {
    Object.assign(navigator, {
      clipboard: { writeText: vi.fn().mockResolvedValue(undefined) },
    });

    server.use(
      http.post(INITIATE_URL, () => {
        return HttpResponse.json({ data: mockDeviceAuth });
      }),
      http.get(STATUS_URL, () => {
        return HttpResponse.json({ data: { status: "pending" } });
      }),
    );

    render(<CodexDeviceCodeModal onClose={vi.fn()} />);

    await waitFor(() => {
      expect(screen.getByText("ABCD-1234")).toBeInTheDocument();
    });

    expect(screen.getByRole("button", { name: /copy/i })).toBeInTheDocument();
    expect(screen.queryByText("Copied")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /copy/i }));

    await waitFor(() => {
      expect(screen.getByText("Copied")).toBeInTheDocument();
    });
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith("ABCD-1234");
  });
});
