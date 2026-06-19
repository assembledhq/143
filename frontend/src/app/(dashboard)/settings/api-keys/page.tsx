"use client";

import { useMemo, useState, type FormEvent } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, KeyRound, Pencil, Plus, Shield, Trash2 } from "lucide-react";

import { EmptyState } from "@/components/empty-state";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { usePageTitle } from "@/hooks/use-page-title";
import { api, ApiError } from "@/lib/api";
import { notify as toast } from "@/lib/notify";
import { queryKeys } from "@/lib/query-keys";
import type { APIClient, APIToken, CreateAPIKeyRequest, CreateAPITokenRequest, ListResponse, Repository } from "@/lib/types";

const SCOPE_GROUPS = [
  {
    label: "Sessions",
    all: "sessions:all",
    scopes: [
      ["sessions:read", "Read sessions"],
      ["sessions:create", "Create sessions"],
      ["sessions:write", "Send messages and retry"],
      ["sessions:cancel", "Cancel or end sessions"],
      ["sessions:publish", "Open PRs and branches"],
    ],
  },
  {
    label: "Automations",
    all: "automations:all",
    scopes: [
      ["automations:read", "Read automations"],
      ["automations:create", "Create automations"],
      ["automations:write", "Edit automations"],
      ["automations:run", "Run automations"],
    ],
  },
  {
    label: "Previews",
    all: "previews:all",
    scopes: [
      ["previews:read", "Read previews"],
      ["previews:create", "Create previews"],
      ["previews:stop", "Stop or restart previews"],
    ],
  },
] as const;

const EXPLICIT_FULL_ACCESS_SCOPES = SCOPE_GROUPS.flatMap((group) => group.scopes.map(([scope]) => scope));

type RevealState = {
  token: string;
  prefix: string;
  scopes: string[];
  repositoryIDs: string[];
} | null;

type TokenDialogState =
  | { mode: "new-key" }
  | { mode: "new-token"; client: APIClient }
  | null;

type FormState = {
  integrationName: string;
  description: string;
  tokenName: string;
  accessMode: "custom" | "full";
  scopes: string[];
  repositoryMode: "all" | "selected";
  repositoryIDs: string[];
  expiration: "one-year" | "ninety-days" | "none" | "custom";
  customExpiration: string;
  ipRestricted: boolean;
  ipAllowlist: string;
};

type FormErrors = Partial<Record<"scopes" | "repositories" | "expiration" | "ipAllowlist", string>>;

function defaultFormState(mode: TokenDialogState): FormState {
  return {
    integrationName: "",
    description: "",
    tokenName: mode?.mode === "new-token" ? "rotation" : "production",
    accessMode: "custom",
    scopes: ["sessions:read", "sessions:create"],
    repositoryMode: "all",
    repositoryIDs: [],
    expiration: "one-year",
    customExpiration: "",
    ipRestricted: false,
    ipAllowlist: "",
  };
}

function expirationToISOString(state: FormState): string | null {
  if (state.expiration === "none") return null;
  if (state.expiration === "custom") return state.customExpiration ? new Date(state.customExpiration).toISOString() : null;
  const expires = new Date();
  if (state.expiration === "ninety-days") {
    expires.setDate(expires.getDate() + 90);
  } else {
    expires.setFullYear(expires.getFullYear() + 1);
  }
  return expires.toISOString();
}

function parseAllowlist(value: string): string[] {
  return value
    .split(/[\n,]+/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function selectedScopes(state: FormState): string[] {
  return state.accessMode === "full" ? EXPLICIT_FULL_ACCESS_SCOPES : state.scopes;
}

function selectedRepositoryIDs(state: FormState): string[] {
  return state.repositoryMode === "all" ? [] : state.repositoryIDs;
}

function validateForm(state: FormState): FormErrors {
  const errors: FormErrors = {};
  if (selectedScopes(state).length === 0) {
    errors.scopes = "Select at least one scope.";
  }
  if (state.repositoryMode === "selected" && state.repositoryIDs.length === 0) {
    errors.repositories = "Select at least one repository or switch to all repositories.";
  }
  if (state.expiration === "custom") {
    const expiresAt = state.customExpiration ? new Date(state.customExpiration) : null;
    if (!expiresAt || Number.isNaN(expiresAt.getTime())) {
      errors.expiration = "Enter a valid custom expiration.";
    } else if (expiresAt <= new Date()) {
      errors.expiration = "Custom expiration must be in the future.";
    }
  }
  if (state.ipRestricted) {
    const entries = parseAllowlist(state.ipAllowlist);
    if (entries.length === 0 || entries.some((entry) => !isValidIPOrCIDR(entry))) {
      errors.ipAllowlist = "Enter valid IP addresses or CIDR ranges.";
    }
  }
  return errors;
}

function isValidIPOrCIDR(value: string): boolean {
  const [address, prefix, extra] = value.split("/");
  if (!address || extra !== undefined) return false;
  const version = ipVersion(address);
  if (version == null) return false;
  if (prefix == null) return true;
  if (!/^\d+$/.test(prefix)) return false;
  const numericPrefix = Number(prefix);
  return version === 4 ? numericPrefix >= 0 && numericPrefix <= 32 : numericPrefix >= 0 && numericPrefix <= 128;
}

function ipVersion(address: string): 4 | 6 | null {
  const parts = address.split(".");
  if (parts.length === 4 && parts.every((part) => /^\d+$/.test(part) && Number(part) >= 0 && Number(part) <= 255 && String(Number(part)) === part)) {
    return 4;
  }
  if (address.includes(":") && /^[0-9a-fA-F:]+$/.test(address) && address.split("::").length <= 2) {
    return 6;
  }
  return null;
}

function formatDate(value?: string): string {
  if (!value) return "Never";
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", year: "numeric" }).format(new Date(value));
}

export default function APIKeysSettingsPage() {
  usePageTitle("API keys");

  const [dialog, setDialog] = useState<TokenDialogState>(null);
  const [form, setForm] = useState<FormState>(defaultFormState(null));
  const [formErrors, setFormErrors] = useState<FormErrors>({});
  const [reveal, setReveal] = useState<RevealState>(null);
  const [disableTarget, setDisableTarget] = useState<APIClient | null>(null);
  const [editTarget, setEditTarget] = useState<APIClient | null>(null);
  const [editName, setEditName] = useState("");
  const [editDescription, setEditDescription] = useState("");
  const [search, setSearch] = useState("");
  const [statusFilter, setStatusFilter] = useState<"all" | "enabled" | "disabled">("all");
  const queryClient = useQueryClient();

  const clientsQuery = useQuery<ListResponse<APIClient>>({
    queryKey: queryKeys.apiKeys.clients,
    queryFn: () => api.apiKeys.listClients(),
  });
  const repositoriesQuery = useQuery<ListResponse<Repository>>({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
  });

  const clients = clientsQuery.data?.data ?? [];
  const repositories = repositoriesQuery.data?.data ?? [];
  const filteredClients = clients.filter((client) => {
    const matchesStatus = statusFilter === "all" || client.status === statusFilter;
    const haystack = `${client.name} ${client.description ?? ""}`.toLowerCase();
    return matchesStatus && haystack.includes(search.trim().toLowerCase());
  });

  const createKey = useMutation({
    mutationFn: (body: CreateAPIKeyRequest) => api.apiKeys.create(body),
    onSuccess: (response) => {
      setReveal({
        token: response.data.token.token,
        prefix: response.data.token.token_prefix,
        scopes: response.data.token.scopes ?? [],
        repositoryIDs: response.data.token.repository_ids ?? [],
      });
      setDialog(null);
      void queryClient.invalidateQueries({ queryKey: queryKeys.apiKeys.clients });
    },
    onError: (error) => showMutationError(error, "API key could not be created."),
  });

  const createToken = useMutation({
    mutationFn: ({ clientId, body }: { clientId: string; body: CreateAPITokenRequest }) => api.apiKeys.createToken(clientId, body),
    onSuccess: (response) => {
      setReveal({
        token: response.data.token,
        prefix: response.data.token_prefix,
        scopes: response.data.scopes ?? [],
        repositoryIDs: response.data.repository_ids ?? [],
      });
      setDialog(null);
      void queryClient.invalidateQueries({ queryKey: queryKeys.apiKeys.tokens(response.data.api_client_id) });
    },
    onError: (error) => showMutationError(error, "API token could not be created."),
  });

  const disableClient = useMutation({
    mutationFn: (clientId: string) => api.apiKeys.disableClient(clientId),
    onSuccess: () => {
      setDisableTarget(null);
      void queryClient.invalidateQueries({ queryKey: queryKeys.apiKeys.clients });
    },
    onError: (error) => showMutationError(error, "API client could not be disabled."),
  });

  const updateClient = useMutation({
    mutationFn: ({ clientId, name, description }: { clientId: string; name: string; description: string }) =>
      api.apiKeys.updateClient(clientId, { name, description }),
    onSuccess: () => {
      setEditTarget(null);
      void queryClient.invalidateQueries({ queryKey: queryKeys.apiKeys.clients });
    },
    onError: (error) => showMutationError(error, "API client could not be updated."),
  });

  function openCreateKey() {
    const next = { mode: "new-key" } as const;
    setForm(defaultFormState(next));
    setFormErrors({});
    setDialog(next);
  }

  function openCreateToken(client: APIClient) {
    const next = { mode: "new-token", client } as const;
    setForm(defaultFormState(next));
    setFormErrors({});
    setDialog(next);
  }

  function openEditClient(client: APIClient) {
    setEditTarget(client);
    setEditName(client.name);
    setEditDescription(client.description ?? "");
  }

  function toggleScope(scope: string) {
    setForm((current) => ({
      ...current,
      scopes: current.scopes.includes(scope)
        ? current.scopes.filter((item) => item !== scope)
        : [...current.scopes, scope],
    }));
  }

  function toggleRepository(id: string) {
    setForm((current) => ({
      ...current,
      repositoryIDs: current.repositoryIDs.includes(id)
        ? current.repositoryIDs.filter((item) => item !== id)
        : [...current.repositoryIDs, id],
    }));
  }

  function setAccessMode(accessMode: FormState["accessMode"]) {
    setForm((current) => ({
      ...current,
      accessMode,
      scopes: accessMode === "full" ? EXPLICIT_FULL_ACCESS_SCOPES : current.scopes,
    }));
  }

  function setRepositoryMode(repositoryMode: FormState["repositoryMode"]) {
    setForm((current) => ({
      ...current,
      repositoryMode,
    }));
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    const errors = validateForm(form);
    setFormErrors(errors);
    if (Object.keys(errors).length > 0) return;
    const allowed_ip_cidrs = form.ipRestricted ? parseAllowlist(form.ipAllowlist) : [];
    const common = {
      token_name: form.tokenName.trim(),
      scopes: selectedScopes(form),
      repository_ids: selectedRepositoryIDs(form),
      expires_at: expirationToISOString(form),
      allowed_ip_cidrs,
    };
    if (dialog?.mode === "new-token") {
      createToken.mutate({
        clientId: dialog.client.id,
        body: {
          name: common.token_name,
          scopes: common.scopes,
          repository_ids: common.repository_ids,
          expires_at: common.expires_at,
          allowed_ip_cidrs: common.allowed_ip_cidrs,
        },
      });
      return;
    }
    createKey.mutate({
      integration_name: form.integrationName.trim(),
      description: form.description.trim() || undefined,
      ...common,
    });
  }

  const creating = createKey.isPending || createToken.isPending;

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="API keys"
          description="Issue scoped service-account keys for CI, internal tools, and AI workflows."
          action={(
            <Button onClick={openCreateKey} className="gap-2">
              <Plus className="h-4 w-4" />
              Create API key
            </Button>
          )}
        />

        {clients.length === 0 && !clientsQuery.isLoading ? (
          <EmptyState
            icon={KeyRound}
            title="No API keys"
            description="Create a service account for systems that need to start sessions, run automations, or manage previews."
            action={{ label: "Create API key", onClick: openCreateKey }}
          />
        ) : (
          <div className="space-y-4">
            <div className="flex flex-col gap-3 sm:flex-row">
              <Input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Search integrations..." className="sm:max-w-sm" />
              <Select value={statusFilter} onValueChange={(value) => setStatusFilter(value as typeof statusFilter)}>
                <SelectTrigger className="sm:w-40">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">All statuses</SelectItem>
                  <SelectItem value="enabled">Enabled</SelectItem>
                  <SelectItem value="disabled">Disabled</SelectItem>
                </SelectContent>
              </Select>
            </div>
            {filteredClients.map((client) => (
              <APIClientRow
                key={client.id}
                client={client}
                onCreateToken={() => openCreateToken(client)}
                onEdit={() => openEditClient(client)}
                onDisable={() => setDisableTarget(client)}
              />
            ))}
            {filteredClients.length === 0 && (
              <p className="rounded-md border border-border p-6 text-center text-sm text-muted-foreground">No API keys match the current filters.</p>
            )}
          </div>
        )}
      </div>

      <Dialog open={dialog != null} onOpenChange={(open) => !open && setDialog(null)}>
        <DialogContent className="max-w-3xl">
          <DialogHeader>
            <DialogTitle>{dialog?.mode === "new-token" ? "Create token" : "Create API key"}</DialogTitle>
            <DialogDescription>
              {dialog?.mode === "new-token" ? `Add a disposable credential under ${dialog.client.name}.` : "Create a service account and its first disposable credential."}
            </DialogDescription>
          </DialogHeader>
          <form onSubmit={submit} className="space-y-5">
            {dialog?.mode === "new-key" && (
              <div className="grid gap-3 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="integration-name">Integration name</Label>
                  <Input id="integration-name" value={form.integrationName} onChange={(event) => setForm({ ...form, integrationName: event.target.value })} required />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="description">Description</Label>
                  <Input id="description" value={form.description} onChange={(event) => setForm({ ...form, description: event.target.value })} />
                </div>
              </div>
            )}
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="token-name">Token name</Label>
                <Input id="token-name" value={form.tokenName} onChange={(event) => setForm({ ...form, tokenName: event.target.value })} required />
              </div>
              <div className="space-y-2">
                <Label>Expiration</Label>
                <RadioGroup value={form.expiration} onValueChange={(value) => setForm({ ...form, expiration: value as FormState["expiration"] })} className="grid grid-cols-2 gap-2">
                  {[
                    ["one-year", "1 year"],
                    ["ninety-days", "90 days"],
                    ["custom", "Custom"],
                    ["none", "No expiration"],
                  ].map(([value, label]) => (
                    <Label key={value} className="flex items-center gap-2 rounded-md border border-border px-3 py-2 text-xs">
                      <RadioGroupItem value={value} />
                      {label}
                    </Label>
                  ))}
                </RadioGroup>
              </div>
            </div>
            {form.expiration === "custom" && (
              <div className="space-y-2">
                <Label htmlFor="custom-expiration">Custom expiration</Label>
                <Input id="custom-expiration" type="datetime-local" value={form.customExpiration} onChange={(event) => setForm({ ...form, customExpiration: event.target.value })} />
                {formErrors.expiration && <p className="text-xs text-destructive">{formErrors.expiration}</p>}
              </div>
            )}
            {form.expiration === "none" && (
              <p className="rounded-md border border-border bg-muted/40 p-3 text-xs text-muted-foreground">
                No-expiration tokens should be reserved for systems with external rotation and monitoring.
              </p>
            )}
            <ScopeControls
              accessMode={form.accessMode}
              selected={form.scopes}
              error={formErrors.scopes}
              onAccessModeChange={setAccessMode}
              onToggle={toggleScope}
            />
            <RepositoryControls
              repositories={repositories}
              mode={form.repositoryMode}
              selected={form.repositoryIDs}
              error={formErrors.repositories}
              onModeChange={setRepositoryMode}
              onToggle={toggleRepository}
            />
            <div className="space-y-3 rounded-md border border-border p-3">
              <Label className="flex items-center gap-2">
                <Checkbox checked={form.ipRestricted} onCheckedChange={(checked) => setForm({ ...form, ipRestricted: checked === true })} />
                <Shield className="h-4 w-4" />
                Restrict by source IP
              </Label>
              {form.ipRestricted && (
                <>
                  <Label htmlFor="ip-allowlist" className="sr-only">Allowed IPs or CIDRs</Label>
                  <Textarea id="ip-allowlist" value={form.ipAllowlist} onChange={(event) => setForm({ ...form, ipAllowlist: event.target.value })} placeholder="203.0.113.10/32&#10;198.51.100.7" />
                  {formErrors.ipAllowlist && <p className="text-xs text-destructive">{formErrors.ipAllowlist}</p>}
                </>
              )}
            </div>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setDialog(null)}>Cancel</Button>
              <Button type="submit" disabled={creating || selectedScopes(form).length === 0}>{creating ? "Creating..." : "Create"}</Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={reveal != null} onOpenChange={(open) => !open && setReveal(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Copy API key</DialogTitle>
            <DialogDescription>This token is shown once. Store it in your secret manager before closing.</DialogDescription>
          </DialogHeader>
          {reveal && (
            <div className="space-y-3">
              <p className="text-xs text-muted-foreground">
                Prefix {reveal.prefix} · {reveal.scopes.length} scopes · {reveal.repositoryIDs.length === 0 ? "All repositories" : `${reveal.repositoryIDs.length} repositories`}
              </p>
              <CopyBlock label="Raw token" value={reveal.token} copyLabel="Copy raw token" />
              <CopyBlock label="Authorization header" value={`Authorization: Bearer ${reveal.token}`} copyLabel="Copy authorization header" />
              <CopyBlock label="Curl example" value={curlExampleForScopes(reveal.token, reveal.scopes)} copyLabel="Copy curl example" />
              <div className="flex flex-wrap gap-2">
                <Button asChild variant="outline" size="sm">
                  <Link href="/docs/reference/external-api">Docs</Link>
                </Button>
                <Button asChild variant="outline" size="sm">
                  <Link href="/api/docs/raw/reference/external-api">Raw docs</Link>
                </Button>
                <Button asChild variant="outline" size="sm">
                  <Link href="/llms.txt">LLMs.txt</Link>
                </Button>
              </div>
              <div className="flex justify-end">
                <Button onClick={() => setReveal(null)}>I have saved it</Button>
              </div>
            </div>
          )}
        </DialogContent>
      </Dialog>

      <Dialog open={editTarget != null} onOpenChange={(open) => !open && setEditTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Edit integration</DialogTitle>
            <DialogDescription>Rename the durable service-account identity shown in audit logs and Settings.</DialogDescription>
          </DialogHeader>
          <form
            className="space-y-4"
            onSubmit={(event) => {
              event.preventDefault();
              if (!editTarget) return;
              updateClient.mutate({ clientId: editTarget.id, name: editName.trim(), description: editDescription.trim() });
            }}
          >
            <div className="space-y-2">
              <Label htmlFor="edit-name">Name</Label>
              <Input id="edit-name" value={editName} onChange={(event) => setEditName(event.target.value)} required />
            </div>
            <div className="space-y-2">
              <Label htmlFor="edit-description">Description</Label>
              <Textarea id="edit-description" value={editDescription} onChange={(event) => setEditDescription(event.target.value)} />
            </div>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setEditTarget(null)}>Cancel</Button>
              <Button type="submit" disabled={updateClient.isPending}>Save</Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <AlertDialog open={disableTarget != null} onOpenChange={(open) => !open && setDisableTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Disable API client?</AlertDialogTitle>
            <AlertDialogDescription>All tokens under this integration will stop authenticating.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => disableTarget && disableClient.mutate(disableTarget.id)}>Disable</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </PageContainer>
  );
}

function APIClientRow({ client, onCreateToken, onEdit, onDisable }: { client: APIClient; onCreateToken: () => void; onEdit: () => void; onDisable: () => void }) {
  const [revokeTarget, setRevokeTarget] = useState<APIToken | null>(null);
  const tokensQuery = useQuery<ListResponse<APIToken>>({
    queryKey: queryKeys.apiKeys.tokens(client.id),
    queryFn: () => api.apiKeys.listTokens(client.id),
  });
  const queryClient = useQueryClient();
  const revokeToken = useMutation({
    mutationFn: (tokenId: string) => api.apiKeys.revokeToken(client.id, tokenId),
    onSuccess: () => {
      setRevokeTarget(null);
      void queryClient.invalidateQueries({ queryKey: queryKeys.apiKeys.tokens(client.id) });
    },
    onError: (error) => showMutationError(error, "API token could not be revoked."),
  });
  const tokens = tokensQuery.data?.data ?? [];
  const activeTokens = tokens.filter((token) => tokenStatus(token).label === "Active").length;

  return (
    <section className="rounded-md border border-border bg-card p-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="space-y-1">
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-semibold text-foreground">{client.name}</h2>
            <Badge variant={client.status === "enabled" ? "default" : "secondary"}>{client.status}</Badge>
          </div>
          {client.description && <p className="text-xs text-muted-foreground">{client.description}</p>}
          <p className="text-xs text-muted-foreground">Created {formatDate(client.created_at)} · {activeTokens} active tokens</p>
        </div>
        <div className="flex gap-2">
          <Button size="sm" variant="outline" onClick={onCreateToken} disabled={client.status === "disabled"}>Create token</Button>
          <Button size="sm" variant="outline" onClick={onEdit}>
            <Pencil className="h-4 w-4" />
          </Button>
          <Button size="sm" variant="outline" onClick={onDisable} disabled={client.status === "disabled"}>Disable</Button>
        </div>
      </div>
      <div className="mt-4 overflow-hidden rounded-md border border-border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Prefix</TableHead>
              <TableHead>Scopes</TableHead>
              <TableHead>Repos</TableHead>
              <TableHead>Last used</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {tokens.map((token) => {
              const status = tokenStatus(token);
              return (
                <TableRow key={token.id}>
                  <TableCell>
                    <div className="space-y-1">
                      <div className="font-medium">{token.name}</div>
                      <Badge variant={status.variant}>{status.label}</Badge>
                      <div className="text-xs text-muted-foreground">Created {formatDate(token.created_at)} · Expires {formatDate(token.expires_at)}</div>
                    </div>
                  </TableCell>
                  <TableCell className="font-mono text-xs">{token.token_prefix}</TableCell>
                  <TableCell>{token.scopes.length} scopes</TableCell>
                  <TableCell>{token.repository_ids.length === 0 ? "All repos" : `${token.repository_ids.length} repos`}</TableCell>
                  <TableCell>
                    <div className="space-y-1 text-xs">
                      <div>{formatDate(token.last_used_at)}</div>
                      {token.last_used_ip && <div className="text-muted-foreground">{token.last_used_ip}</div>}
                      {token.last_used_user_agent && <div className="max-w-48 truncate text-muted-foreground">{token.last_used_user_agent}</div>}
                    </div>
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => setRevokeTarget(token)}
                      disabled={status.label !== "Active" || revokeToken.isPending}
                      aria-label={`Revoke ${token.name}`}
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </TableCell>
                </TableRow>
              );
            })}
            {tokens.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} className="py-6 text-center text-sm text-muted-foreground">No tokens</TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
      <AlertDialog open={revokeTarget != null} onOpenChange={(open) => !open && setRevokeTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Revoke token?</AlertDialogTitle>
            <AlertDialogDescription>
              {revokeTarget ? `${revokeTarget.name} will stop authenticating immediately. Existing sessions are not cancelled.` : "This token will stop authenticating immediately."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => revokeTarget && revokeToken.mutate(revokeTarget.id)}>Revoke token</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}

function ScopeControls({
  accessMode,
  selected,
  error,
  onAccessModeChange,
  onToggle,
}: {
  accessMode: FormState["accessMode"];
  selected: string[];
  error?: string;
  onAccessModeChange: (mode: FormState["accessMode"]) => void;
  onToggle: (scope: string) => void;
}) {
  const fullAccess = accessMode === "full";
  return (
    <div className="space-y-3">
      <Label>Scopes</Label>
      <RadioGroup value={accessMode} onValueChange={(value) => onAccessModeChange(value as FormState["accessMode"])} className="grid gap-2 sm:grid-cols-2">
        <Label className="flex items-start gap-2 rounded-md border border-border px-3 py-2 text-xs">
          <RadioGroupItem value="full" />
          <span>
            <span className="block font-medium text-foreground">Full external API access</span>
            <span className="text-muted-foreground">Expands to every explicit scope. No wildcard is stored.</span>
          </span>
        </Label>
        <Label className="flex items-start gap-2 rounded-md border border-border px-3 py-2 text-xs">
          <RadioGroupItem value="custom" />
          <span>
            <span className="block font-medium text-foreground">Custom access</span>
            <span className="text-muted-foreground">Choose product-family scopes or individual operations.</span>
          </span>
        </Label>
      </RadioGroup>
      <div className="grid gap-3 md:grid-cols-3">
        {SCOPE_GROUPS.map((group) => (
          <div key={group.label} className="space-y-2 rounded-md border border-border p-3">
            <Label className="flex items-center gap-2 text-sm font-medium">
              <Checkbox checked={fullAccess || selected.includes(group.all)} disabled={fullAccess} onCheckedChange={() => onToggle(group.all)} />
              {group.label}:all
            </Label>
            {selected.includes(group.all) && !fullAccess && (
              <p className="text-xs text-muted-foreground">Includes all current {group.label.toLowerCase()} endpoints and future endpoints in this product family.</p>
            )}
            {group.scopes.map(([scope, label]) => (
              <Label key={scope} className="flex items-center gap-2 text-xs text-muted-foreground">
                <Checkbox checked={fullAccess || selected.includes(group.all) || selected.includes(scope)} disabled={fullAccess || selected.includes(group.all)} onCheckedChange={() => onToggle(scope)} />
                {label}
              </Label>
            ))}
          </div>
        ))}
      </div>
      {fullAccess && <p className="text-xs text-muted-foreground">All scope controls are disabled because this key will receive every explicit external API scope.</p>}
      {error && <p className="text-xs text-destructive">{error}</p>}
    </div>
  );
}

function RepositoryControls({
  repositories,
  mode,
  selected,
  error,
  onModeChange,
  onToggle,
}: {
  repositories: Repository[];
  mode: FormState["repositoryMode"];
  selected: string[];
  error?: string;
  onModeChange: (mode: FormState["repositoryMode"]) => void;
  onToggle: (id: string) => void;
}) {
  const [repositorySearch, setRepositorySearch] = useState("");
  const repoOptions = useMemo(() => {
    const needle = repositorySearch.trim().toLowerCase();
    return repositories
      .filter((repo) => repo.full_name.toLowerCase().includes(needle))
      .slice(0, 50);
  }, [repositories, repositorySearch]);
  return (
    <div className="space-y-2">
      <Label>Repository access</Label>
      <RadioGroup value={mode} onValueChange={(value) => onModeChange(value as FormState["repositoryMode"])} className="grid gap-2 sm:grid-cols-2">
        <Label className="flex items-start gap-2 rounded-md border border-border px-3 py-2 text-xs">
          <RadioGroupItem value="all" />
          <span>
            <span className="block font-medium text-foreground">All repositories</span>
            <span className="text-muted-foreground">Allow requests across every connected repository.</span>
          </span>
        </Label>
        <Label className="flex items-start gap-2 rounded-md border border-border px-3 py-2 text-xs">
          <RadioGroupItem value="selected" />
          <span>
            <span className="block font-medium text-foreground">Selected repositories</span>
            <span className="text-muted-foreground">Limit requests to specific repositories.</span>
          </span>
        </Label>
      </RadioGroup>
      {mode === "selected" && (
        <div className="space-y-2">
          <Label htmlFor="repository-search" className="sr-only">Search repositories</Label>
          <Input id="repository-search" value={repositorySearch} onChange={(event) => setRepositorySearch(event.target.value)} placeholder="Search repositories" />
          <div className="grid max-h-40 gap-2 overflow-auto rounded-md border border-border p-3 md:grid-cols-2">
            {repoOptions.map((repo) => (
              <Label key={repo.id} className="flex items-center gap-2 text-xs">
                <Checkbox checked={selected.includes(repo.id)} onCheckedChange={() => onToggle(repo.id)} />
                {repo.full_name}
              </Label>
            ))}
            {repoOptions.length === 0 && <p className="text-xs text-muted-foreground">No repositories match the current search.</p>}
          </div>
        </div>
      )}
      {error && <p className="text-xs text-destructive">{error}</p>}
    </div>
  );
}

function CopyBlock({ label, value, copyLabel }: { label: string; value: string; copyLabel: string }) {
  return (
    <div className="space-y-2">
      <Label>{label}</Label>
      <div className="flex gap-2">
        <div className="min-w-0 flex-1 rounded-md border border-border bg-muted/40 px-3 py-2 font-mono text-xs break-all">
          {value}
        </div>
        <Button type="button" variant="outline" onClick={() => copyText(value)} className="gap-2" aria-label={copyLabel}>
          <Copy className="h-4 w-4" />
          Copy
        </Button>
      </div>
    </div>
  );
}

function curlExampleForScopes(token: string, scopes: string[]): string {
  if (scopes.includes("sessions:create") || scopes.includes("sessions:all")) {
    return `curl -X POST /api/v1/sessions -H 'Authorization: Bearer ${token}' -H 'Content-Type: application/json' -d '{"repository_id":"repo_123","prompt":"Investigate the failing build"}'`;
  }
  if (scopes.includes("automations:run") || scopes.includes("automations:all")) {
    return `curl -X POST /api/v1/automations/automation_123/runs -H 'Authorization: Bearer ${token}'`;
  }
  if (scopes.includes("previews:create") || scopes.includes("previews:all")) {
    return `curl -X POST /api/v1/previews -H 'Authorization: Bearer ${token}' -H 'Content-Type: application/json' -d '{"repository_id":"repo_123","branch":"main"}'`;
  }
  return `curl /api/v1/sessions -H 'Authorization: Bearer ${token}'`;
}

function tokenStatus(token: APIToken): { label: string; variant: "default" | "secondary" | "destructive" | "outline" } {
  if (token.revoked_at) {
    return { label: `Revoked ${formatDate(token.revoked_at)}`, variant: "secondary" };
  }
  if (token.expires_at && new Date(token.expires_at) <= new Date()) {
    return { label: `Expired ${formatDate(token.expires_at)}`, variant: "destructive" };
  }
  return { label: "Active", variant: "default" };
}

function copyText(value: string) {
  void navigator.clipboard
    ?.writeText(value)
    .then(() => toast.success("API key copied"))
    .catch(() => toast.error("Could not copy API key"));
}

function showMutationError(error: unknown, fallback: string) {
  toast.error(error instanceof ApiError ? error.message : fallback);
}
