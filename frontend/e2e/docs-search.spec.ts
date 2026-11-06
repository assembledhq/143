import { expect, test } from "@playwright/test";

test.beforeAll(async ({ request }) => {
  // Next dev compiles the docs and its index on their first requests. Keep
  // that cold-start work outside the interaction test's timeout.
  test.setTimeout(120_000);
  for (const path of ["/docs", "/docs/search-index.json"]) {
    const response = await request.get(path, { timeout: 120_000 });
    expect(response.ok(), `${path} should be served by the frontend`).toBe(true);
  }
});

test("searches and opens public docs without the backend API", async ({ context, page }) => {
  const pageErrors: string[] = [];
  const backendSearchRequests: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));

  // Production sends /api/* to the Go backend. Docs search must keep working
  // when that backend does not serve the old Next search endpoint.
  await context.route(/\/api\/search(?:\?|$)/, async (route) => {
    backendSearchRequests.push(route.request().url());
    await route.fulfill({
      status: 404,
      contentType: "application/json",
      body: JSON.stringify({ error: { code: "NOT_FOUND", message: "Not found" } }),
    });
  });

  await page.goto("/docs");
  const indexResponse = page.waitForResponse(
    (response) => new URL(response.url()).pathname === "/docs/search-index.json",
  );
  await page.getByRole("button", { name: /^(Search|Open Search)/ }).first().click();
  const dialog = page.getByRole("dialog", { name: "Search", exact: true });
  const search = dialog.getByRole("textbox");
  await search.fill("Preview environments");

  expect((await indexResponse).status(), "the real static index should load successfully").toBe(200);
  await expect(dialog.getByRole("button", { name: /Preview environments/ }).first()).toBeVisible();
  await expect(page.getByRole("heading", { name: "Something went wrong", exact: true })).toBeHidden();

  await search.fill("zzzxqvnoindexeddocterm");
  await expect(dialog.getByText("No results found", { exact: true })).toBeVisible();

  await search.fill("Private npm packages");
  await dialog.getByRole("button", { name: /Private npm packages/ }).first().click();
  await expect(page).toHaveURL(/\/docs\/guides\/private-packages$/);
  await expect(page.getByRole("heading", { name: "Private npm packages", exact: true })).toBeVisible();
  await expect(dialog).toBeHidden();
  expect(backendSearchRequests, "docs search must not depend on the backend-owned /api prefix").toEqual([]);
  expect(pageErrors, "searching and opening a result should not crash the page").toEqual([]);
});
