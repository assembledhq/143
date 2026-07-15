import { describe, expect, it } from "vitest";
import { isListResponse } from "./list-response";

describe("isListResponse", () => {
  it.each([
    ["an array-valued data field", { data: [], meta: {} }, true],
    ["extra response fields", { data: [1], extra: true }, true],
    ["an inherited data field", Object.create({ data: [] }), true],
    ["a non-array data field", { data: {} }, false],
    ["a missing data field", { meta: {} }, false],
    ["null", null, false],
    ["an array", [], false],
    ["a primitive", "value", false],
  ])("recognizes %s", (_name, value, expected) => {
    expect(isListResponse(value)).toBe(expected);
  });
});
