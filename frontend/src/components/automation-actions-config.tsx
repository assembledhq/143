"use client";

import { useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

const kinds = [
  ["github_label", "Add a GitHub label"],
  ["github_team_review", "Request a GitHub team review"],
  ["github_issue_comment", "Comment on a GitHub PR"],
  ["notion_tracking_row", "Create a Notion page"],
  ["slack_notification", "Send a Slack message"],
] as const;
type Kind = typeof kinds[number][0];
const propertyTypeOptions = [
  ["title", "Title"],
  ["rich_text", "Rich text"],
  ["url", "URL"],
  ["date", "Date"],
  ["select", "Select"],
] as const;
type PropertyType = typeof propertyTypeOptions[number][0];
const propertyTypes = propertyTypeOptions.map(([value]) => value);
type ActionConfig = {
  actions: Kind[];
  repository: string;
  label: string;
  team: string;
  notion_data_source_id: string;
  slack_channel_id: string;
  notion_properties: Record<string, PropertyType>;
};
const text = (v: unknown) => typeof v === "string" ? v : "";
const bytes = (v: string) => new TextEncoder().encode(v).length;
function readConfig(raw: Record<string, unknown>): ActionConfig {
  const props = raw.notion_properties;
  return {
    actions: Array.isArray(raw.actions) ? raw.actions as Kind[] : [],
    repository: text(raw.repository), label: text(raw.label), team: text(raw.team),
    notion_data_source_id: text(raw.notion_data_source_id), slack_channel_id: text(raw.slack_channel_id),
    notion_properties: props && typeof props === "object" && !Array.isArray(props) ? props as Record<string, PropertyType> : {},
  };
}
export function actionConfigError(raw: Record<string, unknown>): string | null {
  const c = readConfig(raw);
  if (c.actions.length === 0 || c.actions.length > kinds.length || new Set(c.actions).size !== c.actions.length || c.actions.some((k) => !kinds.some(([kind]) => k === kind))) return "Select at least one supported action.";
  if (c.actions.some((k) => k.startsWith("github_")) && (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(c.repository) || bytes(c.repository) > 256 || c.repository.split("/").some((part) => part === "." || part === ".."))) return "Enter a repository as owner/name.";
  if (c.actions.includes("github_label") && (!c.label.trim() || bytes(c.label) > 50 || /[\r\n]/.test(c.label))) return "Enter a GitHub label of at most 50 bytes.";
  if (c.actions.includes("github_team_review") && !/^[a-zA-Z0-9][a-zA-Z0-9-]{0,99}$/.test(c.team)) return "Enter a GitHub team slug.";
  if (c.actions.includes("slack_notification") && !/^[CG][A-Z0-9]{8,31}$/.test(c.slack_channel_id)) return "Enter a Slack channel ID.";
  if (c.actions.includes("notion_tracking_row")) {
    const id = c.notion_data_source_id.replaceAll("-", "");
    if (!/^[0-9a-f]{32}$/i.test(id) || /^0+$/.test(id)) return "Enter a Notion data source UUID.";
    const properties = Object.entries(c.notion_properties);
    if (properties.length === 0 || properties.length > 20 || properties.some(([name, type]) => !name.trim() || bytes(name) > 100 || !propertyTypes.includes(type)) || properties.filter(([, type]) => type === "title").length !== 1) return "Configure 1–20 Notion properties with exactly one title property.";
  }
  return null;
}

export function AutomationActionsConfig({ config, disabled, onSave }: {
  config: Record<string, unknown>;
  disabled?: boolean;
  onSave: (config: Record<string, unknown>) => void;
}) {
  const id = useId();
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState(() => readConfig(config));
  const [properties, setProperties] = useState(() => Object.entries(readConfig(config).notion_properties));
  const configured = { ...draft, notion_properties: Object.fromEntries(properties) };
  const duplicateProperty = new Set(properties.map(([name]) => name)).size !== properties.length;
  const error = duplicateProperty ? "Notion property names must be distinct." : actionConfigError(configured);
  const has = (kind: Kind) => draft.actions.includes(kind);
  const field = (key: "repository" | "label" | "team" | "notion_data_source_id" | "slack_channel_id", label: string, placeholder: string) => (
    <div className="space-y-2" key={key}>
      <Label htmlFor={`${id}-${key}`}>{label}</Label>
      <Input id={`${id}-${key}`} value={draft[key]} placeholder={placeholder} onChange={(event) => setDraft({ ...draft, [key]: event.target.value })} />
    </div>
  );
  return (
    <Dialog open={open} onOpenChange={(value) => {
      if (value) { const next = readConfig(config); setDraft(next); setProperties(Object.entries(next.notion_properties)); }
      setOpen(value);
    }}>
      <DisabledTooltip disabled={!!disabled} content="An organization admin must configure automation actions.">
        <DialogTrigger asChild><Button type="button" variant="outline" size="sm" disabled={disabled}>Configure actions</Button></DialogTrigger>
      </DisabledTooltip>
      <DialogContent className="max-h-[85vh] overflow-y-auto">
        <DialogHeader><DialogTitle>Resumable automation actions</DialogTitle><DialogDescription>Select the actions this automation may perform. Configure only the destinations it needs. Each action keeps its receipt across runs.</DialogDescription></DialogHeader>
        <div className="space-y-3">
          {kinds.map(([kind, label]) => <div key={kind} className="flex items-center gap-2"><Checkbox id={`${id}-${kind}`} checked={has(kind)} onCheckedChange={(checked) => setDraft({ ...draft, actions: checked ? [...draft.actions, kind] : draft.actions.filter((k) => k !== kind) })} /><Label htmlFor={`${id}-${kind}`}>{label}</Label></div>)}
          {draft.actions.some((k) => k.startsWith("github_")) && field("repository", "Repository", "owner/repository")}
          {has("github_label") && field("label", "GitHub label", "needs-review")}
          {has("github_team_review") && field("team", "GitHub team slug", "reviewers")}
          {has("slack_notification") && field("slack_channel_id", "Slack channel ID", "C0123456789")}
          {has("notion_tracking_row") && <>
            {field("notion_data_source_id", "Notion data source ID", "UUID from Manage data sources")}
            <p className="text-sm text-muted-foreground">Allow specific property names and types. Select values must already exist in Notion.</p>
            {properties.map(([name, type], index) => <div key={index} className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(8rem,0.7fr)_auto] sm:items-center">
              <Input aria-label={`Property ${index + 1} name`} value={name} onChange={(event) => setProperties(properties.map((item, i) => i === index ? [event.target.value, type] : item))} />
              <Select value={type} onValueChange={(value: PropertyType) => setProperties(properties.map((item, i) => i === index ? [name, value] : item))}><SelectTrigger aria-label={`Property ${index + 1} type`}><SelectValue /></SelectTrigger><SelectContent>{propertyTypeOptions.map(([value, label]) => <SelectItem key={value} value={value}>{label}</SelectItem>)}</SelectContent></Select>
              <Button type="button" variant="ghost" size="sm" className="justify-self-start text-destructive hover:text-destructive sm:justify-self-auto" aria-label={`Remove property ${index + 1}`} onClick={() => setProperties(properties.filter((_, i) => i !== index))}>Remove</Button>
            </div>)}
            <DisabledTooltip disabled={properties.length >= 20} content="You can configure up to 20 properties."><Button type="button" variant="outline" size="sm" disabled={properties.length >= 20} onClick={() => setProperties([...properties, ["", properties.length === 0 ? "title" : "rich_text"]])}>Add property</Button></DisabledTooltip>
          </>}
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
          <p className="text-sm text-muted-foreground">Changes apply to future runs and block unfinished actions using the old configuration. Completed receipts remain available.</p>
        </div>
        <DialogFooter className="flex-row items-center justify-end gap-2"><Button type="button" variant="outline" onClick={() => setOpen(false)}>Cancel</Button><DisabledTooltip disabled={!!error} content={error ?? ""}><Button type="button" disabled={!!error} onClick={() => { onSave(configured); setOpen(false); }}>Save configuration</Button></DisabledTooltip></DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
