import { capitalizeWords } from "@/lib/utils";

const membershipRoles = new Set(["admin", "builder", "member", "viewer"]);

export function isMembershipRole(role: string): boolean {
  return membershipRoles.has(role);
}

export function roleLabel(role: string): string {
  if (role === "member") {
    return "Engineer";
  }

  return capitalizeWords(role);
}
