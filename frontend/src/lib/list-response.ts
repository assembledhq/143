import type { ListResponse } from "./types";

export function isListResponse<T>(value: unknown): value is ListResponse<T> {
  return (
    typeof value === "object" &&
    value !== null &&
    "data" in value &&
    Array.isArray((value as { data?: unknown }).data)
  );
}
