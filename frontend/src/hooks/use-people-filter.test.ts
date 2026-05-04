import { describe, expect, it } from "vitest";
import { buildFilterSuffix, normalizePeopleFilter, peopleFilterLabel } from "./use-people-filter";
import type { User } from "@/lib/types";

const currentUser = {
  id: "user-1",
  org_id: "org-1",
  email: "me@example.com",
  name: "Ada Lovelace",
  role: "admin",
  created_at: "2026-01-01T00:00:00Z",
} satisfies User;

const members = [
  currentUser,
  {
    id: "user-2",
    org_id: "org-1",
    email: "grace@example.com",
    name: "Grace Hopper",
    role: "member",
    created_at: "2026-01-01T00:00:00Z",
  },
  {
    id: "user-3",
    org_id: "org-1",
    email: "margaret@example.com",
    name: "Margaret Hamilton",
    role: "member",
    created_at: "2026-01-01T00:00:00Z",
  },
] satisfies User[];

describe("usePeopleFilter helpers", () => {
  it("treats an empty filter as Mine", () => {
    const normalized = normalizePeopleFilter(null, currentUser);
    expect(normalized.mode).toBe("mine");
    expect(normalized.serialized).toBeNull();
    expect(normalized.selectedUserIDs).toEqual(["user-1"]);
  });

  it("preserves Everyone", () => {
    const normalized = normalizePeopleFilter("all", currentUser);
    expect(normalized.mode).toBe("all");
    expect(normalized.serialized).toBe("all");
  });

  it("normalizes explicit multi-person selections", () => {
    const normalized = normalizePeopleFilter("user-2,user-3,user-2", currentUser);
    expect(normalized.mode).toBe("custom");
    expect(normalized.serialized).toBe("user-2,user-3");
    expect(normalized.selectedUserIDs).toEqual(["user-2", "user-3"]);
  });

  it("serializes people into filter suffixes", () => {
    expect(buildFilterSuffix("all", "active", "repo-1", "Search")).toBe("?people=all&status=active&repo=repo-1&search=Search");
    expect(buildFilterSuffix("user-2,user-3", null, null, null)).toBe("?people=user-2%2Cuser-3");
    expect(buildFilterSuffix(null, "active", null, null)).toBe("?status=active");
  });

  it("builds compact labels for custom selections", () => {
    expect(peopleFilterLabel("mine", ["user-1"], members, currentUser)).toBe("Mine");
    expect(peopleFilterLabel("all", [], members, currentUser)).toBe("Everyone");
    expect(peopleFilterLabel("custom", ["user-2"], members, currentUser)).toBe("Grace");
    expect(peopleFilterLabel("custom", ["user-2", "user-3"], members, currentUser)).toBe("Grace +1");
  });
});
