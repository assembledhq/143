import { isMembershipRole, roleLabel } from "@/lib/roles";

function isRoleDetailKey(key: string): boolean {
  return key === "role" || key.endsWith("_role");
}

export function formatAuditDetailValue(key: string, value: unknown): string {
  if (typeof value === "string" && isRoleDetailKey(key) && isMembershipRole(value)) {
    return roleLabel(value);
  }

  if (typeof value === "object") {
    return JSON.stringify(value);
  }

  return String(value);
}
