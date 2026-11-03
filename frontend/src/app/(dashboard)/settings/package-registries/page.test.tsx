import { beforeEach, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { act } from "@testing-library/react";
import { queryKeys } from "@/lib/query-keys";
import { createTestQueryClient, fireEvent, renderWithProviders, screen, userEvent, waitFor, within } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import PackageRegistriesPage from "./page";

const auth = vi.hoisted(() => ({ role: "admin" }));
vi.mock("@/hooks/use-auth", () => ({ useAuth: () => ({ user: { role: auth.role }, isLoading: false }) }));
const credential = { id: "credential-1", repository_id: "repo-1", registry_url: "https://registry.example.test", package_scope: "@example", revision: "00000000-0000-0000-0000-000000000001", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" };
const secret = "synthetic-test-secret";
async function openAddDialog() {
  const button = await screen.findByRole("button", { name: "Add credential" });
  await waitFor(() => expect(button).toBeEnabled());
  await userEvent.click(button);
  return screen.getByRole("dialog");
}
function fillCreate(dialog: HTMLElement) {
  fireEvent.change(within(dialog).getByLabelText("Registry URL"), { target: { value: credential.registry_url } });
  fireEvent.change(within(dialog).getByLabelText("Package scope"), { target: { value: credential.package_scope } });
  fireEvent.change(within(dialog).getByLabelText("Token"), { target: { value: secret } });
}
beforeEach(() => {
  auth.role = "admin";
  server.use(
    http.get("*/api/v1/repositories", () => HttpResponse.json({ data: [{ id: "repo-1", full_name: "example/application" }, { id: "repo-2", full_name: "example/documentation" }], meta: {} })),
    http.get("*/api/v1/repositories/:id/package-registry-credentials", ({ params }) => HttpResponse.json({ data: params.id === "repo-1" ? [credential] : [], meta: {} })),
  );
});
describe("PackageRegistriesPage", () => {
  it("lists metadata only and loads credentials for the selected repository", async () => {
    const requests: string[] = [];
    server.use(http.get("*/api/v1/repositories/:id/package-registry-credentials", ({ params }) => { requests.push(String(params.id)); return HttpResponse.json({ data: params.id === "repo-1" ? [credential] : [], meta: {} }); }));
    renderWithProviders(<PackageRegistriesPage />);
    expect(await screen.findByText(credential.registry_url)).toBeInTheDocument();
    expect(screen.getByText("@example")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /reveal/i })).not.toBeInTheDocument();
    expect(screen.queryByText(secret)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("combobox", { name: "Repository" }));
    await userEvent.click(screen.getByRole("option", { name: "example/documentation" }));
    expect(await screen.findByText("No package credentials")).toBeInTheDocument();
    expect(screen.queryByText("@example")).not.toBeInTheDocument();
    expect(requests).toEqual(["repo-1", "repo-2"]);
  });
  it("creates with a password input, clears after success, and never caches the token", async () => {
    const bodies: unknown[] = [];
    server.use(http.post("*/api/v1/repositories/repo-1/package-registry-credentials", async ({ request }) => { bodies.push(await request.json()); return HttpResponse.json({ data: credential }, { status: 201 }); }));
    const queryClient = createTestQueryClient();
    renderWithProviders(<PackageRegistriesPage />, { queryClient });
    const dialog = await openAddDialog(); fillCreate(dialog);
    expect(within(dialog).getByLabelText("Token")).toHaveAttribute("type", "password");
    expect(screen.queryByText(secret)).not.toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Add credential" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(bodies).toEqual([{ registry_url: credential.registry_url, package_scope: credential.package_scope, token: secret }]);
    expect(JSON.stringify(queryClient.getQueryCache().getAll().map((query) => query.state.data))).not.toContain(secret);
    expect(queryClient.getMutationCache().getAll()).toEqual([]);
    expect(within(await openAddDialog()).getByLabelText("Token")).toHaveValue("");
  });
  it("clears the token on cancel and escape before switching repositories", async () => {
    renderWithProviders(<PackageRegistriesPage />);
    let dialog = await openAddDialog(); fillCreate(dialog);
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    dialog = await openAddDialog(); expect(within(dialog).getByLabelText("Token")).toHaveValue(""); fillCreate(dialog);
    fireEvent.keyDown(dialog, { key: "Escape", code: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await userEvent.click(screen.getByRole("combobox", { name: "Repository" }));
    await userEvent.click(screen.getByRole("option", { name: "example/documentation" }));
    expect(within(await openAddDialog()).getByLabelText("Token")).toHaveValue("");
  });
  it.each(["cancel", "escape"])("keeps a newer dialog open when an older save completes after %s", async (dismiss) => {
    let finishRequest: (() => void) | undefined;
    const response = new Promise<Response>((resolve) => {
      finishRequest = () => resolve(HttpResponse.json({ data: credential }, { status: 201 }));
    });
    const createRequest = vi.fn(() => response);
    const listRequest = vi.fn(() => HttpResponse.json({ data: [credential], meta: {} }));
    server.use(
      http.post("*/api/v1/repositories/repo-1/package-registry-credentials", createRequest),
      http.get("*/api/v1/repositories/repo-1/package-registry-credentials", listRequest),
    );
    renderWithProviders(<PackageRegistriesPage />);
    const firstDialog = await openAddDialog();
    fillCreate(firstDialog);
    await userEvent.click(within(firstDialog).getByRole("button", { name: "Add credential" }));
    await waitFor(() => expect(createRequest).toHaveBeenCalledOnce());
    if (dismiss === "cancel") {
      await userEvent.click(within(firstDialog).getByRole("button", { name: "Cancel" }));
    } else {
      fireEvent.keyDown(firstDialog, { key: "Escape", code: "Escape" });
    }
    const secondDialog = await openAddDialog();
    const secondToken = "another-synthetic-test-secret";
    fireEvent.change(within(secondDialog).getByLabelText("Token"), { target: { value: secondToken } });
    await act(async () => finishRequest?.());
    await waitFor(() => expect(listRequest).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("dialog")).toBe(secondDialog);
    expect(within(secondDialog).getByLabelText("Token")).toHaveValue(secondToken);
  });
  it("discards an open token form when the repository changes", async () => {
    const queryClient = createTestQueryClient();
    renderWithProviders(<PackageRegistriesPage />, { queryClient });
    fillCreate(await openAddDialog());
    act(() => queryClient.setQueryData(queryKeys.repositories.all, { data: [{ id: "repo-2", full_name: "example/documentation" }], meta: {} }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(within(await openAddDialog()).getByLabelText("Token")).toHaveValue("");
  });
  it("rotates the token with its revision and deletes only after confirmation", async () => {
    const updates: unknown[] = []; const deleted: string[] = [];
    server.use(
      http.put("*/api/v1/repositories/repo-1/package-registry-credentials/credential-1", async ({ request }) => { updates.push(await request.json()); return HttpResponse.json({ data: credential }); }),
      http.delete("*/api/v1/repositories/repo-1/package-registry-credentials/credential-1", ({ request }) => { deleted.push(new URL(request.url).searchParams.get("expected_revision") ?? ""); return new HttpResponse(null, { status: 204 }); }),
    );
    renderWithProviders(<PackageRegistriesPage />);
    await userEvent.click(await screen.findByRole("button", { name: "Rotate @example credential" }));
    const dialog = screen.getByRole("dialog"); expect(within(dialog).queryByLabelText("Registry URL")).not.toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText("Token"), { target: { value: secret } });
    await userEvent.click(within(dialog).getByRole("button", { name: "Rotate credential" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(updates).toEqual([{ expected_revision: credential.revision, token: secret }]);
    await userEvent.click(screen.getByRole("button", { name: "Delete @example credential" })); expect(deleted).toEqual([]);
    await userEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Delete credential" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument()); expect(deleted).toEqual([credential.revision]);
  });
  it.each([["http://registry.example.test", "@example"], ["https://registry.example.test/packages", "@example"], ["https://user:password@registry.example.test", "@example"], ["https://registry.example.test", "example"], ["https://registry.example.test", "@Example"], ["https://registry.example.test:8443", "@example"], ["https://127.0.0.1", "@example"], ["https://registry.example.test?", "@example"], ["https://registry.example.test", "@" + "a".repeat(215)]])("rejects invalid registry or scope: %s %s", async (registry, scope) => {
    renderWithProviders(<PackageRegistriesPage />); const dialog = await openAddDialog(); fillCreate(dialog);
    fireEvent.change(within(dialog).getByLabelText("Registry URL"), { target: { value: registry } }); fireEvent.change(within(dialog).getByLabelText("Package scope"), { target: { value: scope } });
    expect(within(dialog).getByRole("button", { name: "Add credential" })).toBeDisabled();
  });
  it.each(["member", "viewer", "builder"])("denies %s without requesting credential data", (role) => {
    auth.role = role; const get = vi.fn(); server.use(http.get("*/api/v1/repositories/:id/package-registry-credentials", () => { get(); return HttpResponse.json({ data: [], meta: {} }); }));
    renderWithProviders(<PackageRegistriesPage />); expect(screen.getByRole("alert")).toHaveTextContent("Administrator access is required"); expect(screen.queryByRole("combobox")).not.toBeInTheDocument(); expect(get).not.toHaveBeenCalled();
  });
  it("uses the standard error notice and retries repository loading", async () => {
    const repositories = vi.fn()
      .mockReturnValueOnce(HttpResponse.json({ error: { code: "INTERNAL", message: "unavailable" } }, { status: 500 }))
      .mockReturnValue(HttpResponse.json({ data: [{ id: "repo-1", full_name: "example/application" }], meta: {} }));
    server.use(http.get("*/api/v1/repositories", repositories));

    renderWithProviders(<PackageRegistriesPage />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Repositories could not be loaded");
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));

    expect(await screen.findByRole("combobox", { name: "Repository" })).toBeInTheDocument();
    expect(repositories).toHaveBeenCalledTimes(2);
  });
  it("uses the standard error notice and retries credential loading", async () => {
    const credentials = vi.fn()
      .mockReturnValueOnce(HttpResponse.json({ error: { code: "INTERNAL", message: "unavailable" } }, { status: 500 }))
      .mockReturnValue(HttpResponse.json({ data: [credential], meta: {} }));
    server.use(http.get("*/api/v1/repositories/:id/package-registry-credentials", credentials));

    renderWithProviders(<PackageRegistriesPage />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Package credentials could not be loaded");
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));

    expect(await screen.findByText(credential.registry_url)).toBeInTheDocument();
    expect(credentials).toHaveBeenCalledTimes(2);
  });
  it.each([[503, "PACKAGE_REGISTRY_ENCRYPTION_UNAVAILABLE", "Package credential storage is unavailable. Try again later."], [403, "FORBIDDEN", "Administrator access is required to manage package credentials."], [409, "PACKAGE_REGISTRY_CREDENTIAL_EXISTS", "This registry origin or package scope already has a credential. Rotate the existing credential instead."], [409, "CONFLICT", "This credential changed. Refresh the page before trying again."], [500, "INTERNAL", "Unable to update the package credential. Try again."]])("shows a safe error for status %s", async (status, code, message) => {
    server.use(http.post("*/api/v1/repositories/repo-1/package-registry-credentials", () => HttpResponse.json({ error: { code, message: secret } }, { status: Number(status) })));
    renderWithProviders(<PackageRegistriesPage />); const dialog = await openAddDialog(); fillCreate(dialog);
    await userEvent.click(within(dialog).getByRole("button", { name: "Add credential" })); expect(await screen.findByRole("alert")).toHaveTextContent(String(message)); expect(screen.queryByText(secret)).not.toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" })); expect(within(await openAddDialog()).getByLabelText("Token")).toHaveValue("");
  });
});
