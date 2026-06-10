"use client";

import { useState } from "react";
import { GripVertical, KeyRound, MoveUp, MoveDown, Pencil } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/empty-state";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { AGENTS_BY_KEY } from "@/lib/agents";
import { cn } from "@/lib/utils";
import type { CodingCredentialSummary } from "@/lib/types";

type MoveDirection = "up" | "down";
export type DropPosition = "before" | "after";

function agentLabel(agent: CodingCredentialSummary["agent"]) {
  return AGENTS_BY_KEY[agent]?.label ?? agent;
}

function authTypeLabel(type: CodingCredentialSummary["auth_type"]) {
  return type === "subscription" ? "Subscription" : "API key";
}

function statusLabel(status: CodingCredentialSummary["status"]) {
  switch (status) {
    case "healthy":
      return "Healthy";
    case "rate_limited":
      return "Rate limited";
    case "needs_reauth":
      return "Needs reauth";
    case "invalid":
      return "Invalid";
    default:
      return status;
  }
}

function statusBadgeClass(status: CodingCredentialSummary["status"]) {
  switch (status) {
    case "healthy":
      return "border-success/30 bg-success/10 text-success";
    case "rate_limited":
      return "border-warning/30 bg-warning/10 text-warning";
    case "needs_reauth":
    case "invalid":
      return "border-destructive/30 bg-destructive/10 text-destructive";
    default:
      return "";
  }
}

function rateLimitNote(row: CodingCredentialSummary) {
  if (row.status !== "rate_limited") return null;
  if (row.rate_limited_until) {
    return `Available again ${new Date(row.rate_limited_until).toLocaleTimeString([], {
      hour: "numeric",
      minute: "2-digit",
    })}`;
  }
  return row.rate_limit_message ?? null;
}

export function CodingAuthStack({
  rows,
  selectedId,
  onSelect,
  onMove,
  onReorder,
}: {
  rows: CodingCredentialSummary[];
  selectedId: string | null;
  onSelect: (id: string) => void;
  onMove: (id: string, direction: MoveDirection) => void;
  onReorder: (sourceId: string, targetId: string, position: DropPosition) => void;
}) {
  const [draggingId, setDraggingId] = useState<string | null>(null);
  const [dragOver, setDragOver] = useState<{ id: string; position: DropPosition } | null>(null);

  if (rows.length === 0) {
    return (
      <div className="overflow-hidden rounded-2xl border border-border bg-card">
        <EmptyState
          variant="inline"
          icon={KeyRound}
          title="No org coding auths yet"
          description="Add an org-level auth so coding-agent sessions have shared fallback credentials."
        />
      </div>
    );
  }

  return (
    <div className="overflow-hidden rounded-2xl border border-border bg-card">
      <div className="divide-y divide-border md:hidden">
        {rows.map((row, index) => (
          <div
            key={row.id}
            className={cn("space-y-3 px-4 py-4", selectedId === row.id ? "bg-muted/40" : "")}
          >
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0 space-y-1">
                <Button
                  type="button"
                  variant="ghost"
                  className="h-auto justify-start px-0 py-0 text-left font-medium text-foreground hover:bg-transparent"
                  onClick={() => onSelect(row.id)}
                >
                  {row.label}
                </Button>
                <div className="text-xs text-muted-foreground">{agentLabel(row.agent)}</div>
              </div>
              <div className="flex flex-wrap justify-end gap-2">
                <Badge variant="outline" className={statusBadgeClass(row.status)}>
                  {statusLabel(row.status)}
                </Badge>
                {row.is_default ? <Badge>Default</Badge> : null}
              </div>
            </div>
            <dl className="grid grid-cols-2 gap-3 text-xs">
              <div className="space-y-1">
                <dt className="font-medium text-muted-foreground">Priority</dt>
                <dd className="flex items-center gap-2 text-foreground">
                  <span>{row.priority}</span>
                  <GripVertical className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
                </dd>
              </div>
              <div className="space-y-1">
                <dt className="font-medium text-muted-foreground">Auth type</dt>
                <dd className="text-foreground">{authTypeLabel(row.auth_type)}</dd>
              </div>
              <div className="space-y-1">
                <dt className="font-medium text-muted-foreground">Status</dt>
                <dd className="text-foreground">{statusLabel(row.status)}</dd>
              </div>
              <div className="space-y-1">
                <dt className="font-medium text-muted-foreground">Agent</dt>
                <dd className="text-foreground">{agentLabel(row.agent)}</dd>
              </div>
            </dl>
            {(row.usage_note || rateLimitNote(row)) ? (
              <div className="space-y-1">
                <div className="text-xs font-medium text-muted-foreground">Notes</div>
                {row.usage_note ? <div className="text-xs text-muted-foreground">{row.usage_note}</div> : null}
                {rateLimitNote(row) ? <div className="text-xs text-muted-foreground">{rateLimitNote(row)}</div> : null}
              </div>
            ) : null}
            <div className="flex items-center gap-1">
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
              <Button
                type="button"
                variant="ghost"
                size="icon"
                aria-label={`Edit ${row.label}`}
                onClick={() => onSelect(row.id)}
              >
                <Pencil className="h-4 w-4" />
              </Button>
            </div>
          </div>
        ))}
      </div>

      <div className="hidden md:block">
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
            {rows.map((row, index) => {
              const showDropAbove = dragOver?.id === row.id && dragOver.position === "before";
              const showDropBelow = dragOver?.id === row.id && dragOver.position === "after";
              return (
                <TableRow
                  key={row.id}
                  draggable
                  onDragStart={(event) => {
                    setDraggingId(row.id);
                    event.dataTransfer.effectAllowed = "move";
                    event.dataTransfer.setData("text/plain", row.id);
                  }}
                  onDragOver={(event) => {
                    if (!draggingId || draggingId === row.id) return;
                    event.preventDefault();
                    event.dataTransfer.dropEffect = "move";
                    const rect = event.currentTarget.getBoundingClientRect();
                    const position: DropPosition =
                      event.clientY < rect.top + rect.height / 2 ? "before" : "after";
                    setDragOver((prev) =>
                      prev?.id === row.id && prev.position === position
                        ? prev
                        : { id: row.id, position },
                    );
                  }}
                  onDragLeave={(event) => {
                    if (event.currentTarget.contains(event.relatedTarget as Node | null)) return;
                    if (dragOver?.id === row.id) setDragOver(null);
                  }}
                  onDrop={(event) => {
                    event.preventDefault();
                    if (draggingId && draggingId !== row.id && dragOver?.id === row.id) {
                      onReorder(draggingId, row.id, dragOver.position);
                    }
                    setDraggingId(null);
                    setDragOver(null);
                  }}
                  onDragEnd={() => {
                    setDraggingId(null);
                    setDragOver(null);
                  }}
                  className={cn(
                    selectedId === row.id ? "bg-muted/40" : "",
                    draggingId === row.id ? "opacity-40" : "",
                    showDropAbove ? "border-t-2 border-t-primary" : "",
                    showDropBelow ? "border-b-2 border-b-primary" : "",
                  )}
                >
                  <TableCell>
                    <div className="flex h-9 cursor-grab items-center gap-2 rounded-lg border border-border bg-muted/30 px-3 text-sm font-medium text-foreground active:cursor-grabbing">
                      <span>{row.priority}</span>
                      <GripVertical className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
                    </div>
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
                      {rateLimitNote(row) ? <div className="text-xs text-muted-foreground">{rateLimitNote(row)}</div> : null}
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
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        aria-label={`Edit ${row.label}`}
                        onClick={() => onSelect(row.id)}
                      >
                        <Pencil className="h-4 w-4" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}
