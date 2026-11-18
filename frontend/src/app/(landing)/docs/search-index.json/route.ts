import { createFromSource } from "fumadocs-core/search/server";
import { source } from "@/lib/source";

// Keep the docs index outside /api, which production routes to the Go backend.
export const revalidate = false;

export const { staticGET: GET } = createFromSource(source, {
  language: "english",
});
