"use client";

import { useQueryClient } from "@tanstack/react-query";
import { AlertCircle, CheckCircle2, Clock3, Plus, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ClaudeCodeAuthModal } from "@/components/claude-code-auth-modal";
import type { ClaudeCodeSubscription } from "@/lib/types";

// ClaudeSubscriptionManager renders the shared "connected subscriptions list"
// plus the "add a subscription" form + OAuth modal. Used by both the
// standalone agent settings page and the embedded AgentSettingsEditor so the
// two surfaces don't drift on copy, layout, or the React Query invalidation
// key. Pass onRemove to opt into per-row delete controls; omit it to hide
// them (the embedded editor hides remove to keep the card simple).
export function ClaudeSubscriptionManager({
  subscriptions,
  label,
  onLabelChange,
  showModal,
  onOpenModal,
  onCloseModal,
  onRemove,
  connectedLabelText,
  addButtonVariant = "plain",
}: {
  subscriptions: ClaudeCodeSubscription[];
  label: string;
  onLabelChange: (value: string) => void;
  showModal: boolean;
  onOpenModal: () => void;
  onCloseModal: () => void;
  onRemove?: (sub: ClaudeCodeSubscription) => void;
  // Overrides the "Connected subscriptions (N)" label — lets the page view
  // append its "usage is distributed via round-robin" hint without forcing
  // that copy onto the compact editor variant.
  connectedLabelText?: (count: number) => string;
  // "plain" = label only (embedded editor); "plus" = Plus icon + label
  // (standalone page). No behavioral difference.
  addButtonVariant?: "plain" | "plus";
}) {
  const queryClient = useQueryClient();
  const active = subscriptions.filter((s) => s.status === "active");
  const recoverable = subscriptions.filter(
    (s) => s.status === "pending_auth" || s.status === "invalid",
  );
  const connectedText = connectedLabelText
    ? connectedLabelText(active.length)
    : `Connected subscriptions (${active.length})`;

  return (
    <div className="space-y-3">
      {active.length > 0 && (
        <div className="space-y-2">
          <Label className="text-xs text-muted-foreground">{connectedText}</Label>
          {active.map((sub) => (
            <SubscriptionRow key={sub.id} sub={sub} onRemove={onRemove} />
          ))}
        </div>
      )}

      {recoverable.length > 0 && (
        <div className="space-y-2">
          <Label className="text-xs text-muted-foreground">
            Needs attention ({recoverable.length})
          </Label>
          {recoverable.map((sub) => (
            <SubscriptionRow key={sub.id} sub={sub} onRemove={onRemove} />
          ))}
        </div>
      )}

      <div className="flex items-center gap-2">
        <Input
          placeholder="Subscription label (e.g. Team A)"
          value={label}
          onChange={(e) => onLabelChange(e.target.value.slice(0, 100))}
          maxLength={100}
          className="max-w-xs text-sm"
        />
        <Button
          size="sm"
          onClick={onOpenModal}
          disabled={showModal || label.trim() === ""}
        >
          {addButtonVariant === "plus" && <Plus className="mr-1 h-3.5 w-3.5" />}
          Add subscription
        </Button>
      </div>

      {showModal && label.trim() !== "" && (
        <ClaudeCodeAuthModal
          label={label.trim()}
          onClose={onCloseModal}
          onConnected={() => {
            queryClient.invalidateQueries({ queryKey: ["claude-code-subscriptions"] });
            onCloseModal();
          }}
        />
      )}
    </div>
  );
}

function SubscriptionRow({
  sub,
  onRemove,
}: {
  sub: ClaudeCodeSubscription;
  onRemove?: (sub: ClaudeCodeSubscription) => void;
}) {
  const badge =
    sub.status === "active"
      ? {
          label: "Active",
          className: "border-success text-success",
          icon: CheckCircle2,
        }
      : sub.status === "pending_auth"
        ? {
            label: "Pending auth",
            className: "border-warning text-warning",
            icon: Clock3,
          }
        : {
            label: "Invalid",
            className: "border-destructive text-destructive",
            icon: AlertCircle,
          };
  const Icon = badge.icon;

  return (
    <div className="flex items-center justify-between rounded-md border px-3 py-2">
      <div className="flex items-center gap-2">
        <Badge variant="outline" className={badge.className}>
          <Icon className="mr-1 h-3.5 w-3.5" />
          {badge.label}
        </Badge>
        <span className="text-sm font-medium">{sub.label}</span>
        {sub.account_type && (
          <span className="text-xs text-muted-foreground">({sub.account_type})</span>
        )}
      </div>
      {onRemove && (
        <Button
          size="sm"
          variant="ghost"
          className="text-xs text-muted-foreground hover:text-destructive"
          onClick={() => onRemove(sub)}
          aria-label={`Remove Claude subscription ${sub.label}`}
        >
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
      )}
    </div>
  );
}
