"use client";

import { AuditLogTrigger } from "@/components/audit/audit-log-trigger";
import type { AuditResourceType, User } from "@/lib/types";

type ActivityScope = { resource_type: AuditResourceType };

interface SettingsLastActivityProps {
  scopes: ActivityScope | ActivityScope[];
  members?: User[];
  title: string;
}

export function SettingsLastActivity({ scopes, members, title }: SettingsLastActivityProps) {
  return <AuditLogTrigger filters={scopes} members={members} title={title} variant="footer" />;
}
