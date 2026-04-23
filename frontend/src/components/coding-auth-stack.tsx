"use client";

import { GripVertical, MoveUp, MoveDown, ChevronsUp } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { AGENTS_BY_KEY } from "@/lib/agents";
import type { CodingAuth } from "@/lib/types";

type MoveDirection = "up" | "down";

function agentLabel(agent: CodingAuth["agent"]) {
  return AGENTS_BY_KEY[agent]?.label ?? agent;
}

function authTypeLabel(type: CodingAuth["auth_type"]) {
  return type === "subscription" ? "Subscription" : "API key";
}

function statusLabel(status: CodingAuth["status"]) {
  switch (status) {
    case "healthy":
      return "Healthy";
    case "rate_limited":
      return "Rate limited";
    case "needs_reauth":
      return "Needs reauth";
    case "invalid":
      return "Invalid";
    case "never_verified":
      return "Never verified";
    default:
      return status;
  }
}

function statusBadgeClass(status: CodingAuth["status"]) {
  switch (status) {
    case "healthy":
      return "border-emerald-500/30 bg-emerald-500/10 text-emerald-700";
    case "rate_limited":
      return "border-amber-500/30 bg-amber-500/10 text-amber-700";
    case "needs_reauth":
    case "invalid":
      return "border-red-500/30 bg-red-500/10 text-red-700";
    case "never_verified":
      return "border-slate-500/30 bg-slate-500/10 text-slate-700";
    default:
      return "";
  }
}

export function CodingAuthStack({
  rows,
  selectedId,
  onSelect,
  onMove,
  onMoveToTop,
}: {
  rows: CodingAuth[];
  selectedId: string | null;
  onSelect: (id: string) => void;
  onMove: (id: string, direction: MoveDirection) => void;
  onMoveToTop: (id: string) => void;
}) {
  return (
    <div className="overflow-hidden rounded-2xl border border-border bg-card">
      <Table className="table-fixed">
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="w-[120px]">Priority</TableHead>
            <TableHead className="w-[140px]">Agent</TableHead>
            <TableHead className="w-[140px]">Auth type</TableHead>
            <TableHead className="w-auto">Label</TableHead>
            <TableHead className="w-[180px]">Status</TableHead>
            <TableHead className="w-[150px] text-right">Actions</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((row, index) => (
            <TableRow
              key={row.id}
              className={selectedId === row.id ? "bg-muted/40" : ""}
            >
              <TableCell>
                <Button
                  type="button"
                  variant="ghost"
                  className="h-9 rounded-lg border border-border bg-muted/30 px-3 py-0 text-sm font-medium text-foreground hover:bg-muted/60"
                  onClick={() => onSelect(row.id)}
                >
                  <span>{row.priority}</span>
                  <GripVertical className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
                </Button>
              </TableCell>
              <TableCell className="font-medium whitespace-normal">{agentLabel(row.agent)}</TableCell>
              <TableCell>{authTypeLabel(row.auth_type)}</TableCell>
              <TableCell className="whitespace-normal">
                <div className="space-y-1">
                  <Button
                    type="button"
                    variant="ghost"
                    className="h-auto justify-start px-0 py-0 font-medium text-foreground hover:bg-transparent"
                    onClick={() => onSelect(row.id)}
                  >
                    {row.label}
                  </Button>
                  {row.usage_note ? <div className="text-xs text-muted-foreground">{row.usage_note}</div> : null}
                </div>
              </TableCell>
              <TableCell>
                <div className="flex items-center gap-2">
                  <Badge variant="outline" className={statusBadgeClass(row.status)}>
                    {statusLabel(row.status)}
                  </Badge>
                  {row.is_default ? <Badge>Default</Badge> : null}
                </div>
              </TableCell>
              <TableCell>
                <div className="flex items-center justify-end gap-1">
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    aria-label={`Move ${row.label} to top`}
                    onClick={() => onMoveToTop(row.id)}
                    disabled={index === 0}
                  >
                    <ChevronsUp className="h-4 w-4" />
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    aria-label={`Move ${row.label} up`}
                    onClick={() => onMove(row.id, "up")}
                    disabled={index === 0}
                  >
                    <MoveUp className="h-4 w-4" />
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    aria-label={`Move ${row.label} down`}
                    onClick={() => onMove(row.id, "down")}
                    disabled={index === rows.length - 1}
                  >
                    <MoveDown className="h-4 w-4" />
                  </Button>
                </div>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
