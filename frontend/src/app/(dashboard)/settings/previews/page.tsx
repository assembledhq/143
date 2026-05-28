"use client";

import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useQueryState } from "nuqs";
import { Copy, KeyRound, Pencil, Plus, TestTube2, Trash2 } from "lucide-react";

import { EmptyState } from "@/components/empty-state";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { usePageTitle } from "@/hooks/use-page-title";
import { api, ApiError } from "@/lib/api";
import { notify as toast } from "@/lib/notify";
import { queryKeys } from "@/lib/query-keys";
import type {
  ListResponse,
  PreviewAPIToken,
  PreviewSecretBundleOutput,
  PreviewSecretBundleSummary,
  PreviewSecretBundleUpsertRequest,
  Repository,
} from "@/lib/types";

const SCOPES = ["previews:create", "previews:read", "previews:stop"] as const;

type SecretValueRow = {
  /** Stable identity used as a React key — never sent to the server. */
  rowId: string;
  key: string;
  value: string;
  exposeAsEnv: boolean;
};

type BundleDialogMode =
  | { type: "create" }
  | { type: "edit"; bundle: PreviewSecretBundleSummary };

type BundleFormState = {
  name: string;
  rows: SecretValueRow[];
  fileOutputsJSON: string;
};

/** Creates a new blank row with a stable unique ID for React reconciliation. */
function makeRow(overrides?: Partial<Omit<SecretValueRow, "rowId">>): SecretValueRow {
  return { rowId: crypto.randomUUID(), key: "", value: "", exposeAsEnv: true, ...overrides };
}

function makeEmptyBundleForm(): BundleFormState {
  return { name: "", rows: [makeRow()], fileOutputsJSON: "" };
}

export default function PreviewSettingsPage() {
  usePageTitle("Preview");

  return (
    <PageContainer size="default">
      <div className="space-y-8">
        <PageHeader title="Preview" description="Configure preview secrets and API access." />
        <PreviewSecretsSection />
        <PreviewAPISection />
      </div>
    </PageContainer>
  );
}

function PreviewSecretsSection() {
  const queryClient = useQueryClient();
  const [repoParam, setRepoParam] = useQueryState("repo");
  const [selectedRepositoryId, setSelectedRepositoryId] = useState(repoParam ?? "");
  const [dialogMode, setDialogMode] = useState<BundleDialogMode | null>(null);
  const [form, setForm] = useState<BundleFormState>(makeEmptyBundleForm);
  const [formError, setFormError] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<PreviewSecretBundleSummary | null>(null);

  const repositoriesQuery = useQuery<ListResponse<Repository>>({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
  });

  const activeRepositories = useMemo(
    () => (repositoriesQuery.data?.data ?? []).filter((repo) => repo.status === "active"),
    [repositoriesQuery.data?.data],
  );
  // selectedRepositoryId is initialized from repoParam, so the first find already
  // handles the URL-param case. The fallback to activeRepositories[0] picks the
  // first active repo when no explicit selection has been made.
  const selectedRepository = activeRepositories.find((repo) => repo.id === selectedRepositoryId)
    ?? activeRepositories[0]
    ?? null;
  const effectiveSelectedRepositoryId = selectedRepository?.id ?? "";

  useEffect(() => {
    if (!repositoriesQuery.data) return;
    if (selectedRepository && repoParam !== selectedRepository.id) {
      void setRepoParam(selectedRepository.id);
    } else if (!selectedRepository && repoParam) {
      void setRepoParam(null);
    }
  }, [repoParam, repositoriesQuery.data, selectedRepository, setRepoParam]);

  const bundlesQuery = useQuery<ListResponse<PreviewSecretBundleSummary>>({
    queryKey: effectiveSelectedRepositoryId
      ? queryKeys.repositories.previewSecretBundles(effectiveSelectedRepositoryId)
      : queryKeys.repositories.previewSecretBundles("none"),
    queryFn: () => api.repositories.previewSecretBundles.list(effectiveSelectedRepositoryId),
    enabled: Boolean(effectiveSelectedRepositoryId),
  });

  const bundles = bundlesQuery.data?.data ?? [];

  const saveMutation = useMutation({
    mutationFn: ({ mode, body, repositoryId }: { mode: BundleDialogMode; body: PreviewSecretBundleUpsertRequest; repositoryId: string }) => {
      if (mode.type === "edit") {
        return api.repositories.previewSecretBundles.patch(mode.bundle.id, body);
      }
      return api.repositories.previewSecretBundles.upsert(repositoryId, body);
    },
    onSuccess: (_data, { repositoryId }) => {
      toast.success("Preview secret bundle saved");
      closeBundleDialog();
      void queryClient.invalidateQueries({ queryKey: queryKeys.repositories.previewSecretBundles(repositoryId) });
    },
    onError: (error) => {
      setFormError(error instanceof ApiError ? error.message : "Preview secret bundle could not be saved.");
    },
  });

  const testMutation = useMutation({
    mutationFn: (bundleId: string) => api.repositories.previewSecretBundles.test(bundleId),
    onSuccess: (response) => {
      if (response.data.status === "ready") {
        toast.success("Preview secret bundle is ready");
      } else {
        toast.error(response.data.error || "Preview secret bundle test failed");
      }
    },
    onError: (error) => {
      toast.error(error instanceof ApiError ? error.message : "Could not test preview secret bundle");
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (bundle: PreviewSecretBundleSummary) =>
      api.repositories.previewSecretBundles.delete(bundle.repository_id, bundle.name),
    onSuccess: (_response, bundle) => {
      toast.success("Preview secret bundle deleted");
      setDeleteTarget(null);
      void queryClient.invalidateQueries({ queryKey: queryKeys.repositories.previewSecretBundles(bundle.repository_id) });
    },
    onError: (error) => {
      toast.error(error instanceof ApiError ? error.message : "Could not delete preview secret bundle");
    },
  });

  function openCreateDialog() {
    setDialogMode({ type: "create" });
    setForm(makeEmptyBundleForm());
    setFormError(null);
  }

  function openEditDialog(bundle: PreviewSecretBundleSummary) {
    setDialogMode({ type: "edit", bundle });
    const fileOuts = fileOutputsFromBundle(bundle);
    const envRows = envNamesFromBundle(bundle).map((key) => makeRow({ key, exposeAsEnv: true }));
    setForm({
      name: bundle.name,
      rows: envRows.length > 0 ? envRows : [makeRow({ exposeAsEnv: false })],
      fileOutputsJSON: fileOuts.length > 0 ? JSON.stringify(fileOuts, null, 2) : "",
    });
    setFormError(null);
  }

  function closeBundleDialog() {
    setDialogMode(null);
    setForm(makeEmptyBundleForm());
    setFormError(null);
  }

  function updateRow(index: number, patch: Partial<SecretValueRow>) {
    setForm((current) => ({
      ...current,
      rows: current.rows.map((row, rowIndex) => rowIndex === index ? { ...row, ...patch } : row),
    }));
  }

  function addRow() {
    setForm((current) => ({ ...current, rows: [...current.rows, makeRow()] }));
  }

  function removeRow(index: number) {
    setForm((current) => ({
      ...current,
      rows: current.rows.length === 1
        ? [makeRow()]
        : current.rows.filter((_, rowIndex) => rowIndex !== index),
    }));
  }

  function handleSave(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!dialogMode) return;
    const body = buildBundleRequest(form);
    if (body instanceof Error) {
      setFormError(body.message);
      return;
    }
    setFormError(null);
    saveMutation.mutate({ mode: dialogMode, body, repositoryId: effectiveSelectedRepositoryId });
  }

  return (
    <section className="space-y-4" aria-labelledby="preview-secrets-heading">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="space-y-1">
          <h2 id="preview-secrets-heading" className="text-sm font-semibold text-foreground">Preview secrets</h2>
          <p className="text-xs text-muted-foreground">Repo-scoped secret bundles used at preview runtime.</p>
        </div>
        <Button type="button" onClick={openCreateDialog} disabled={!effectiveSelectedRepositoryId}>
          <Plus className="h-4 w-4" />
          New bundle
        </Button>
      </div>

      <div className="max-w-md space-y-1.5">
        <Label htmlFor="preview-repository-select">Repository</Label>
        <Select
          value={effectiveSelectedRepositoryId}
          onValueChange={(value) => {
            setSelectedRepositoryId(value);
            void setRepoParam(value);
          }}
          disabled={activeRepositories.length === 0}
        >
          <SelectTrigger id="preview-repository-select">
            <SelectValue placeholder={repositoriesQuery.isLoading ? "Loading repositories..." : "No active repositories"} />
          </SelectTrigger>
          <SelectContent>
            {activeRepositories.map((repo) => (
              <SelectItem key={repo.id} value={repo.id}>{repo.full_name}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {!repositoriesQuery.isLoading && activeRepositories.length === 0 ? (
        <div className="rounded-md border border-border">
          <EmptyState
            icon={KeyRound}
            title="No active repositories"
            description="Connect a repository before adding preview secret bundles."
            variant="inline"
          />
        </div>
      ) : (
        <BundleInventory
          bundles={bundles}
          isLoading={repositoriesQuery.isLoading || bundlesQuery.isLoading}
          repositoryName={selectedRepository?.full_name ?? ""}
          onCreate={openCreateDialog}
          onEdit={openEditDialog}
          onTest={(bundle) => testMutation.mutate(bundle.id)}
          onDelete={setDeleteTarget}
          testing={testMutation.isPending}
        />
      )}

      <BundleDialog
        mode={dialogMode}
        form={form}
        repositoryName={selectedRepository?.full_name ?? ""}
        error={formError}
        saving={saveMutation.isPending}
        onOpenChange={(open) => {
          if (!open) closeBundleDialog();
        }}
        onFormChange={setForm}
        onRowChange={updateRow}
        onRowAdd={addRow}
        onRowRemove={removeRow}
        onTest={(bundle) => testMutation.mutate(bundle.id)}
        testing={testMutation.isPending}
        onSubmit={handleSave}
      />

      <AlertDialog open={Boolean(deleteTarget)} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete preview secret bundle?</AlertDialogTitle>
            <AlertDialogDescription>
              Delete {deleteTarget?.name} from {selectedRepository?.full_name ?? "this repository"}. Previews that reference this bundle may fail to start.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => deleteTarget && deleteMutation.mutate(deleteTarget)}
              disabled={deleteMutation.isPending}
            >
              Delete bundle
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}

function BundleInventory({
  bundles,
  isLoading,
  repositoryName,
  onCreate,
  onEdit,
  onTest,
  onDelete,
  testing,
}: {
  bundles: PreviewSecretBundleSummary[];
  isLoading: boolean;
  repositoryName: string;
  onCreate: () => void;
  onEdit: (bundle: PreviewSecretBundleSummary) => void;
  onTest: (bundle: PreviewSecretBundleSummary) => void;
  onDelete: (bundle: PreviewSecretBundleSummary) => void;
  testing: boolean;
}) {
  if (isLoading) {
    return (
      <div className="rounded-md border border-border px-4 py-8 text-center text-xs text-muted-foreground">
        Loading preview secret bundles...
      </div>
    );
  }

  if (bundles.length === 0) {
    return (
      <div className="rounded-md border border-border">
        <EmptyState
          icon={KeyRound}
          title="No preview secret bundles"
          description={repositoryName ? `Create the first bundle for ${repositoryName}.` : "Choose a repository to manage preview secrets."}
          variant="inline"
          action={repositoryName ? { label: "New bundle", onClick: onCreate } : undefined}
        />
      </div>
    );
  }

  return (
    <>
      <div className="hidden rounded-md border border-border md:block">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Bundle</TableHead>
              <TableHead>Outputs</TableHead>
              <TableHead>Last changed</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {bundles.map((bundle) => (
              <TableRow key={bundle.id}>
                <TableCell>
                  <div className="min-w-0">
                    <p className="truncate font-medium text-foreground">{bundle.name}</p>
                    <p className="text-xs text-muted-foreground">{bundle.source_type}</p>
                  </div>
                </TableCell>
                <TableCell>
                  <OutputBadges bundle={bundle} />
                </TableCell>
                <TableCell>{formatDate(bundle.created_at)}</TableCell>
                <TableCell>
                  <BundleActions bundle={bundle} onEdit={onEdit} onTest={onTest} onDelete={onDelete} testing={testing} align="end" />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>

      <div className="space-y-3 md:hidden">
        {bundles.map((bundle) => (
          <div key={bundle.id} className="rounded-md border border-border p-3">
            <div className="space-y-3">
              <div className="min-w-0">
                <p className="truncate text-sm font-medium text-foreground">{bundle.name}</p>
                <p className="text-xs text-muted-foreground">{bundle.source_type}</p>
              </div>
              <LabeledMobileValue label="Outputs"><OutputBadges bundle={bundle} /></LabeledMobileValue>
              <LabeledMobileValue label="Last changed">{formatDate(bundle.created_at)}</LabeledMobileValue>
              <BundleActions bundle={bundle} onEdit={onEdit} onTest={onTest} onDelete={onDelete} testing={testing} />
            </div>
          </div>
        ))}
      </div>
    </>
  );
}

function BundleActions({
  bundle,
  onEdit,
  onTest,
  onDelete,
  testing,
  align = "start",
}: {
  bundle: PreviewSecretBundleSummary;
  onEdit: (bundle: PreviewSecretBundleSummary) => void;
  onTest: (bundle: PreviewSecretBundleSummary) => void;
  onDelete: (bundle: PreviewSecretBundleSummary) => void;
  testing: boolean;
  align?: "start" | "end";
}) {
  return (
    <div className={`flex flex-wrap gap-2${align === "end" ? " justify-end" : ""}`}>
      <Button type="button" variant="outline" size="sm" onClick={() => onTest(bundle)} disabled={testing} aria-label={`Test ${bundle.name}`}>
        <TestTube2 className="h-4 w-4" />
        Test
      </Button>
      <Button type="button" variant="outline" size="sm" onClick={() => onEdit(bundle)} aria-label={`Edit ${bundle.name}`}>
        <Pencil className="h-4 w-4" />
        Edit
      </Button>
      <Button type="button" variant="outline" size="sm" onClick={() => onDelete(bundle)} aria-label={`Delete ${bundle.name}`} title={`Delete ${bundle.name}`}>
        <Trash2 className="h-4 w-4" />
        Delete
      </Button>
    </div>
  );
}

function BundleDialog({
  mode,
  form,
  repositoryName,
  error,
  saving,
  onOpenChange,
  onFormChange,
  onRowChange,
  onRowAdd,
  onRowRemove,
  onTest,
  testing,
  onSubmit,
}: {
  mode: BundleDialogMode | null;
  form: BundleFormState;
  repositoryName: string;
  error: string | null;
  saving: boolean;
  onOpenChange: (open: boolean) => void;
  onFormChange: (form: BundleFormState) => void;
  onRowChange: (index: number, patch: Partial<SecretValueRow>) => void;
  onRowAdd: () => void;
  onRowRemove: (index: number) => void;
  onTest: (bundle: PreviewSecretBundleSummary) => void;
  testing: boolean;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
}) {
  const isEdit = mode?.type === "edit";
  const editBundle = mode?.type === "edit" ? mode.bundle : null;
  const editHasFileOutputs = editBundle ? editBundle.outputs.some((o) => o.type === "file") : false;
  const hasFilledValue = form.rows.some((row) => row.key.trim() && row.value);
  const hasOutput = form.rows.some((row) => row.key.trim() && row.value && row.exposeAsEnv) || form.fileOutputsJSON.trim().length > 0;
  const canSave = Boolean(form.name.trim()) && hasFilledValue && hasOutput;
  const saveTooltip = isEdit && !hasFilledValue
    ? "Re-enter at least one secret value to save changes"
    : !hasOutput
      ? "At least one env or file output is required"
      : undefined;

  return (
    <Dialog open={Boolean(mode)} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{isEdit ? "Edit bundle" : "New bundle"}</DialogTitle>
          <DialogDescription>
            Secret values are only sent when you save and are not shown again after creation.
            {editHasFileOutputs ? " This bundle has file outputs — re-enter the content mapping in the field below to preserve it." : null}
          </DialogDescription>
        </DialogHeader>
        <form className="space-y-5" onSubmit={onSubmit}>
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="bundle-repository">Repository</Label>
              <Input id="bundle-repository" value={repositoryName} disabled />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="bundle-name">Bundle name</Label>
              <Input
                id="bundle-name"
                value={form.name}
                onChange={(event) => onFormChange({ ...form, name: event.target.value })}
                placeholder="assembled-dev"
                autoComplete="off"
              />
            </div>
          </div>

          <div className="space-y-2">
            <div>
              <Label>Secret values</Label>
              <p className="text-xs text-muted-foreground">Editing requires re-entering the values you want this bundle to contain.</p>
            </div>
            <div className="space-y-2">
              {form.rows.map((row, index) => (
                <div key={row.rowId} className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto_auto]">
                  <Input
                    value={row.key}
                    onChange={(event) => onRowChange(index, { key: normalizeEnvKey(event.target.value) })}
                    placeholder="API_TOKEN"
                    aria-label={index === 0 ? "Secret key" : `Secret key ${index + 1}`}
                    autoComplete="off"
                  />
                  <Input
                    value={row.value}
                    onChange={(event) => onRowChange(index, { value: event.target.value })}
                    placeholder="Secret value"
                    type="password"
                    aria-label={index === 0 ? "Secret value" : `Secret value ${index + 1}`}
                    autoComplete="new-password"
                  />
                  <Label className="flex h-9 items-center gap-2 rounded-md border border-border px-3 text-xs">
                    <Checkbox
                      checked={row.exposeAsEnv}
                      onCheckedChange={(checked) => onRowChange(index, { exposeAsEnv: checked === true })}
                      aria-label={index === 0 ? "Expose as env" : `Expose as env ${index + 1}`}
                    />
                    Env output
                  </Label>
                  <Button type="button" variant="outline" size="icon" onClick={() => onRowRemove(index)} aria-label={`Remove secret row ${index + 1}`}>
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              ))}
            </div>
            <Button type="button" variant="outline" size="sm" onClick={onRowAdd}>
              <Plus className="h-4 w-4" />
              Add value
            </Button>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="file-outputs">File outputs JSON</Label>
            <Textarea
              id="file-outputs"
              value={form.fileOutputsJSON}
              onChange={(event) => onFormChange({ ...form, fileOutputsJSON: event.target.value })}
              placeholder={'[{"type":"file","path":"development.conf.json","format":"json","content":{"token":"secret:API_TOKEN"}}]'}
              className="min-h-24 font-mono text-xs"
              spellCheck={false}
            />
          </div>

          {error ? <p className="text-sm text-destructive">{error}</p> : null}

          <DialogFooter>
            {isEdit ? (
              <Button
                type="button"
                variant="outline"
                onClick={() => onTest(mode.bundle)}
                disabled={testing}
              >
                <TestTube2 className="h-4 w-4" />
                Test bundle
              </Button>
            ) : null}
            <DialogClose asChild>
              <Button type="button" variant="outline" disabled={saving}>Cancel</Button>
            </DialogClose>
            <Button type="submit" disabled={!canSave || saving} title={saveTooltip}>
              <KeyRound className="h-4 w-4" />
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function PreviewAPISection() {
  const queryClient = useQueryClient();
  const [dialogOpen, setDialogOpen] = useState(false);
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<string[]>([...SCOPES]);
  const [repositoryIDs, setRepositoryIDs] = useState<string[]>([]);
  const [createdToken, setCreatedToken] = useState("");

  const tokensQuery = useQuery<ListResponse<PreviewAPIToken>>({
    queryKey: queryKeys.previews.apiTokens,
    queryFn: () => api.previews.apiTokens.list(),
  });
  const repositoriesQuery = useQuery<ListResponse<Repository>>({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
  });

  const repositories = repositoriesQuery.data?.data ?? [];
  const tokens = tokensQuery.data?.data ?? [];

  const createToken = useMutation({
    mutationFn: () => api.previews.apiTokens.create({ name: name.trim(), scopes, repository_ids: repositoryIDs }),
    onSuccess: (response) => {
      setCreatedToken(response.data.token);
      setName("");
      setScopes([...SCOPES]);
      setRepositoryIDs([]);
      void queryClient.invalidateQueries({ queryKey: queryKeys.previews.apiTokens });
    },
  });

  const revokeToken = useMutation({
    mutationFn: (id: string) => api.previews.apiTokens.revoke(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.previews.apiTokens });
    },
  });

  function toggleScope(scope: string) {
    setScopes((current) => current.includes(scope) ? current.filter((item) => item !== scope) : [...current, scope]);
  }

  function toggleRepository(id: string) {
    setRepositoryIDs((current) => current.includes(id) ? current.filter((item) => item !== id) : [...current, id]);
  }

  function resetTokenDialog(open: boolean) {
    setDialogOpen(open);
    if (!open) {
      setName("");
      setScopes([...SCOPES]);
      setRepositoryIDs([]);
      setCreatedToken("");
      createToken.reset();
    }
  }

  function copyCreatedToken() {
    if (!createdToken) return;
    void navigator.clipboard?.writeText(createdToken)
      .then(() => toast.success("Preview API token copied"))
      .catch(() => toast.error("Could not copy preview API token"));
  }

  return (
    <section className="space-y-4" aria-labelledby="preview-api-heading">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="space-y-1">
          <h2 id="preview-api-heading" className="text-sm font-semibold text-foreground">Preview API</h2>
          <p className="text-xs text-muted-foreground">Scoped tokens for branch and pull request preview automation.</p>
        </div>
        <Button type="button" variant="outline" onClick={() => resetTokenDialog(true)}>
          <Plus className="h-4 w-4" />
          Create token
        </Button>
      </div>

      <TokenInventory
        tokens={tokens}
        repositories={repositories}
        isLoading={tokensQuery.isLoading}
        onRevoke={(token) => revokeToken.mutate(token.id)}
        revoking={revokeToken.isPending}
      />

      <Dialog open={dialogOpen} onOpenChange={resetTokenDialog}>
        <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>Create token</DialogTitle>
            <DialogDescription>The token value is shown once after creation.</DialogDescription>
          </DialogHeader>
          <div className="space-y-5">
            <div className="space-y-1.5">
              <Label htmlFor="preview-token-name">Name</Label>
              <Input id="preview-token-name" value={name} onChange={(event) => setName(event.target.value)} placeholder="CI previews" />
            </div>

            <div className="space-y-2">
              <Label>Scopes</Label>
              <div className="grid gap-2 md:grid-cols-3">
                {SCOPES.map((scope) => (
                  <Label key={scope} className="flex items-center gap-2 rounded-md border border-border px-3 py-2 text-sm">
                    <Checkbox id={`scope-${scope}`} checked={scopes.includes(scope)} onCheckedChange={() => toggleScope(scope)} />
                    {scope}
                  </Label>
                ))}
              </div>
            </div>

            <div className="space-y-2">
              <Label>Repository access</Label>
              <div className="grid max-h-56 gap-2 overflow-auto rounded-md border border-border p-2 md:grid-cols-2">
                {repositories.map((repo) => (
                  <Label key={repo.id} htmlFor={`repo-${repo.id}`} className="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm">
                    <Checkbox id={`repo-${repo.id}`} checked={repositoryIDs.includes(repo.id)} onCheckedChange={() => toggleRepository(repo.id)} />
                    <span className="truncate">{repo.full_name}</span>
                  </Label>
                ))}
              </div>
              <p className="text-xs text-muted-foreground">Leave every repository unchecked to allow all repositories.</p>
            </div>

            {createdToken ? (
              <div className="space-y-1.5 rounded-md border border-border bg-muted/30 p-3">
                <p className="text-xs font-medium text-foreground">One-time token</p>
                <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
                  <p className="break-all font-mono text-xs text-foreground">{createdToken}</p>
                  <Button type="button" variant="outline" size="sm" onClick={copyCreatedToken} aria-label="Copy token">
                    <Copy className="h-4 w-4" />
                    Copy
                  </Button>
                </div>
              </div>
            ) : null}
            {createToken.isError ? (
              <p className="text-sm text-destructive">{createToken.error instanceof Error ? createToken.error.message : "Token could not be created."}</p>
            ) : null}
          </div>
          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline">Cancel</Button>
            </DialogClose>
            <Button type="button" onClick={() => createToken.mutate()} disabled={!name.trim() || scopes.length === 0 || createToken.isPending}>
              <KeyRound className="h-4 w-4" />
              Create token
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}

function TokenInventory({
  tokens,
  repositories,
  isLoading,
  onRevoke,
  revoking,
}: {
  tokens: PreviewAPIToken[];
  repositories: Repository[];
  isLoading: boolean;
  onRevoke: (token: PreviewAPIToken) => void;
  revoking: boolean;
}) {
  if (isLoading) {
    return (
      <div className="rounded-md border border-border px-4 py-8 text-center text-xs text-muted-foreground">
        Loading preview API tokens...
      </div>
    );
  }

  if (tokens.length === 0) {
    return (
      <div className="rounded-md border border-border">
        <EmptyState
          icon={KeyRound}
          title="No preview API tokens"
          description="Create a token when external automation needs preview access."
          variant="inline"
        />
      </div>
    );
  }

  return (
    <>
      <div className="hidden rounded-md border border-border md:block">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Token</TableHead>
              <TableHead>Scopes</TableHead>
              <TableHead>Repository access</TableHead>
              <TableHead>Last used</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {tokens.map((token) => (
              <TableRow key={token.id}>
                <TableCell className="font-medium">{token.name}</TableCell>
                <TableCell>
                  <div className="flex flex-wrap gap-1">
                    {token.scopes.map((scope) => <Badge key={scope} variant="secondary">{scope}</Badge>)}
                  </div>
                </TableCell>
                <TableCell><RepositoryAccessBadge token={token} repositories={repositories} /></TableCell>
                <TableCell>{token.last_used_at ? formatDate(token.last_used_at) : "Never"}</TableCell>
                <TableCell className="text-right">
                  <Button type="button" variant="outline" size="sm" onClick={() => onRevoke(token)} disabled={revoking} aria-label={`Revoke ${token.name}`}>
                    <Trash2 className="h-4 w-4" />
                    Revoke
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>

      <div className="space-y-3 md:hidden">
        {tokens.map((token) => (
          <div key={token.id} className="rounded-md border border-border p-3">
            <div className="space-y-3">
              <p className="truncate text-sm font-medium text-foreground">{token.name}</p>
              <LabeledMobileValue label="Scopes">
                <div className="flex flex-wrap gap-1">
                  {token.scopes.map((scope) => <Badge key={scope} variant="secondary">{scope}</Badge>)}
                </div>
              </LabeledMobileValue>
              <LabeledMobileValue label="Repository access">
                <RepositoryAccessBadge token={token} repositories={repositories} />
              </LabeledMobileValue>
              <LabeledMobileValue label="Last used">{token.last_used_at ? formatDate(token.last_used_at) : "Never"}</LabeledMobileValue>
              <Button type="button" variant="outline" size="sm" onClick={() => onRevoke(token)} disabled={revoking} aria-label={`Revoke ${token.name}`}>
                <Trash2 className="h-4 w-4" />
                Revoke
              </Button>
            </div>
          </div>
        ))}
      </div>
    </>
  );
}

function LabeledMobileValue({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="space-y-1">
      <p className="text-xs font-medium text-muted-foreground">{label}</p>
      <div className="text-sm text-foreground">{children}</div>
    </div>
  );
}

function OutputBadges({ bundle }: { bundle: PreviewSecretBundleSummary }) {
  const outputs = bundle.outputs.flatMap(formatOutputSummary);
  return (
    <div className="flex flex-wrap gap-1">
      {outputs.length > 0
        ? outputs.map((output, idx) => <Badge key={`${output}-${idx}`} variant="secondary">{output}</Badge>)
        : <Badge variant="secondary">No outputs</Badge>}
    </div>
  );
}

function buildBundleRequest(form: BundleFormState): PreviewSecretBundleUpsertRequest | Error {
  const name = form.name.trim();
  if (!name) {
    return new Error("Bundle name is required.");
  }

  const values: Record<string, string> = {};
  for (const row of form.rows) {
    const key = row.key.trim();
    if (!key && !row.value) continue;
    if (!key || !row.value) {
      return new Error("Each secret value needs both a key and a value.");
    }
    values[key] = row.value;
  }
  if (Object.keys(values).length === 0) {
    return new Error("At least one secret value is required.");
  }

  let fileOutputs: PreviewSecretBundleOutput[] = [];
  if (form.fileOutputsJSON.trim()) {
    try {
      const parsed = JSON.parse(form.fileOutputsJSON) as unknown;
      if (!Array.isArray(parsed)) {
        return new Error("File outputs JSON must be an array.");
      }
      fileOutputs = parsed as PreviewSecretBundleOutput[];
    } catch {
      return new Error("File outputs JSON is invalid.");
    }
  }

  // `envValues` maps key → "secret:<key>" reference strings. These are distinct
  // from `source.values` above, which holds the plaintext secret values. Both
  // share the parameter name "values" in the API, but serve different roles:
  // source.values are encrypted at rest; output.values are resolver references.
  const envValues = form.rows.reduce<Record<string, string>>((acc, row) => {
    const key = row.key.trim();
    if (key && row.value && row.exposeAsEnv) {
      acc[key] = `secret:${key}`;
    }
    return acc;
  }, {});
  const outputs: PreviewSecretBundleOutput[] = [
    ...(Object.keys(envValues).length > 0 ? [{ type: "env" as const, values: envValues }] : []),
    ...fileOutputs,
  ];

  if (outputs.length === 0) {
    return new Error("At least one env or file output is required.");
  }

  return {
    name,
    source: { type: "managed", values },
    outputs,
    exposure_policy: "preview_runtime",
  };
}

function envNamesFromBundle(bundle: PreviewSecretBundleSummary): string[] {
  return Array.from(new Set(bundle.outputs.flatMap((output) => output.type === "env" ? output.env ?? [] : [])));
}

function fileOutputsFromBundle(bundle: PreviewSecretBundleSummary): PreviewSecretBundleOutput[] {
  // The list-API summary includes path and format but omits `content` (the resolver
  // reference map). Users editing a bundle with file outputs must re-enter the
  // content field. The dialog description calls this out when file outputs are present.
  return bundle.outputs
    .filter((output) => output.type === "file")
    .map((output) => ({
      type: "file",
      path: output.path,
      format: output.format as PreviewSecretBundleOutput["format"],
    }));
}

function formatOutputSummary(output: PreviewSecretBundleSummary["outputs"][number]): string[] {
  if (output.type === "env") {
    return (output.env?.length ? output.env : ["values"]).map((name) => `env ${name}`);
  }
  if (output.type === "file") {
    return [`${output.format || "raw"} ${output.path || "file"}`];
  }
  return [output.type];
}

function repositoryAccessLabel(token: PreviewAPIToken, repositories: Repository[]): string {
  if (token.repository_ids.length === 0) return "All repositories";
  // Fall back to the raw ID for any repo that has been deleted so the label
  // stays accurate even when repositories are no longer in the fetched list.
  const resolvedNames = token.repository_ids.map(
    (id) => repositories.find((repo) => repo.id === id)?.full_name ?? id,
  );
  if (resolvedNames.length <= 2) {
    return resolvedNames.join(", ");
  }
  return `${token.repository_ids.length} repositories`;
}

function RepositoryAccessBadge({ token, repositories }: { token: PreviewAPIToken; repositories: Repository[] }) {
  return <Badge variant="secondary">{repositoryAccessLabel(token, repositories)}</Badge>;
}

function normalizeEnvKey(value: string): string {
  return value.toUpperCase().replace(/[^A-Z0-9_]/g, "_");
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", year: "numeric" }).format(new Date(value));
}
