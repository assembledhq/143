"use client";

import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useQueryState } from "nuqs";
import { Copy, Eye, HelpCircle, KeyRound, Pencil, Plus, Trash2 } from "lucide-react";

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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { usePageTitle } from "@/hooks/use-page-title";
import { api, ApiError } from "@/lib/api";
import { notify as toast } from "@/lib/notify";
import { queryKeys } from "@/lib/query-keys";
import type {
  ListResponse,
  PreviewAPIToken,
  PreviewSecretBundleRevealResult,
  PreviewSecretBundleOutput,
  PreviewSecretBundlePatchRequest,
  PreviewSecretBundleSummary,
  PreviewSecretBundleUpsertRequest,
  Repository,
} from "@/lib/types";

const SCOPES = ["previews:create", "previews:read", "previews:stop"] as const;

const SECRET_FILE_KEY = "SECRET_FILE_CONTENT";
const JSON_FILE_VALIDATION_DEBOUNCE_MS = 400;
const SECRET_FILE_JSON_ERROR = "Secret file contents must be valid JSON.";
const MASKED_SECRET_PLACEHOLDER = "********";
const MASKED_SECRET_FILE_PLACEHOLDER = `${MASKED_SECRET_PLACEHOLDER}\n${MASKED_SECRET_PLACEHOLDER}\n${MASKED_SECRET_PLACEHOLDER}`;

type SecretValueRow = {
  /** Stable identity used as a React key — never sent to the server. */
  rowId: string;
  key: string;
  value: string;
};

type BundleDialogMode =
  | { type: "create" }
  | { type: "edit"; bundle: PreviewSecretBundleSummary };

type BundleDeliveryMode = "env" | "file";

type BundleFormState = {
  repositoryId: string;
  name: string;
  deliveryMode: BundleDeliveryMode;
  rows: SecretValueRow[];
  filePath: string;
  fileFormat: "raw" | "json";
  fileContent: string;
};

type RevealTarget =
  | { type: "file"; bundle: PreviewSecretBundleSummary }
  | { type: "env"; bundle: PreviewSecretBundleSummary; rowId: string; key: string };

/** Creates a new blank row with a stable unique ID for React reconciliation. */
function makeRow(overrides?: Partial<Omit<SecretValueRow, "rowId">>): SecretValueRow {
  return { rowId: crypto.randomUUID(), key: "", value: "", ...overrides };
}

function makeEmptyBundleForm(repositoryId = ""): BundleFormState {
  return {
    repositoryId,
    name: "",
    deliveryMode: "env",
    rows: [makeRow()],
    filePath: "",
    fileFormat: "raw",
    fileContent: "",
  };
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
  const [jsonValidationError, setJSONValidationError] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<PreviewSecretBundleSummary | null>(null);
  const [revealedEnvRowIds, setRevealedEnvRowIds] = useState<Map<string, string>>(() => new Map());

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
    mutationFn: ({ mode, body, repositoryId }: {
      mode: BundleDialogMode;
      body: PreviewSecretBundlePatchRequest | PreviewSecretBundleUpsertRequest;
      repositoryId: string;
    }) => {
      if (mode.type === "edit") {
        return api.repositories.previewSecretBundles.patch(mode.bundle.id, body);
      }
      return api.repositories.previewSecretBundles.upsert(repositoryId, body as PreviewSecretBundleUpsertRequest);
    },
    onSuccess: (_data, { repositoryId }) => {
      toast.success("Preview secret bundle saved");
      setSelectedRepositoryId(repositoryId);
      void setRepoParam(repositoryId);
      closeBundleDialog();
      void queryClient.invalidateQueries({ queryKey: queryKeys.repositories.previewSecretBundles(repositoryId) });
    },
    onError: (error) => {
      setFormError(error instanceof ApiError ? error.message : "Preview secret bundle could not be saved.");
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

  const revealMutation = useMutation({
    mutationFn: async (target: RevealTarget) => {
      const response = await api.repositories.previewSecretBundles.reveal(target.bundle.id);
      return { response, target };
    },
    onSuccess: ({ response, target }) => {
      if (dialogMode?.type !== "edit" || dialogMode.bundle.id !== target.bundle.id) return;
      const content = getRevealedSecretValue(response.data, target);
      if (content === null) {
        setFormError(target.type === "file"
          ? "Could not find stored file contents for this bundle."
          : `Could not find stored value for ${target.key}.`);
        return;
      }
      if (target.type === "file") {
        setForm((current) => ({ ...current, fileContent: content }));
      } else {
        setForm((current) => ({
          ...current,
          rows: current.rows.map((row) => row.rowId === target.rowId ? { ...row, value: content } : row),
        }));
        setRevealedEnvRowIds((current) => new Map(current).set(target.rowId, target.key));
      }
      setFormError(null);
      setJSONValidationError(null);
    },
    onError: (error, target) => {
      if (dialogMode?.type !== "edit" || dialogMode.bundle.id !== target.bundle.id) return;
      setFormError(error instanceof ApiError ? error.message : "Secret contents could not be revealed.");
    },
  });

  function openCreateDialog() {
    setDialogMode({ type: "create" });
    setForm(makeEmptyBundleForm(effectiveSelectedRepositoryId));
    setFormError(null);
    setJSONValidationError(null);
  }

  function openEditDialog(bundle: PreviewSecretBundleSummary) {
    setDialogMode({ type: "edit", bundle });
    const fileOuts = fileOutputsFromBundle(bundle);
    const envRows = envNamesFromBundle(bundle).map((key) => makeRow({ key }));
    setForm({
      repositoryId: bundle.repository_id,
      name: bundle.name,
      deliveryMode: fileOuts.length > 0 && envRows.length === 0 ? "file" : "env",
      rows: envRows.length > 0 ? envRows : [makeRow()],
      filePath: fileOuts[0]?.path ?? "",
      fileFormat: fileOuts[0]?.format === "json" ? "json" : "raw",
      fileContent: "",
    });
    setFormError(null);
    setJSONValidationError(null);
    setRevealedEnvRowIds(new Map());
  }

  function closeBundleDialog() {
    setDialogMode(null);
    setForm(makeEmptyBundleForm(effectiveSelectedRepositoryId));
    setFormError(null);
    setJSONValidationError(null);
    setRevealedEnvRowIds(new Map());
    revealMutation.reset();
  }

  useEffect(() => {
    const timeoutID = window.setTimeout(() => {
      if (!dialogMode || form.fileFormat !== "json" || !form.fileContent) {
        setJSONValidationError(null);
        return;
      }
      try {
        JSON.parse(form.fileContent);
        setJSONValidationError(null);
      } catch {
        setJSONValidationError(SECRET_FILE_JSON_ERROR);
      }
    }, JSON_FILE_VALIDATION_DEBOUNCE_MS);

    return () => window.clearTimeout(timeoutID);
  }, [dialogMode, form.fileContent, form.fileFormat]);

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
    const removed = form.rows[index];
    setForm((current) => ({
      ...current,
      rows: current.rows.length === 1
        ? [makeRow()]
        : current.rows.filter((_, rowIndex) => rowIndex !== index),
    }));
    if (removed) {
      setRevealedEnvRowIds((ids) => {
        if (!ids.has(removed.rowId)) return ids;
        const next = new Map(ids);
        next.delete(removed.rowId);
        return next;
      });
    }
  }

  function handleSave(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!dialogMode) return;
    const body = buildBundleRequest(form, dialogMode);
    if (body instanceof Error) {
      if (body.message === SECRET_FILE_JSON_ERROR) {
        setJSONValidationError(SECRET_FILE_JSON_ERROR);
        setFormError(null);
      } else {
        setFormError(body.message);
      }
      return;
    }
    if (!form.repositoryId) {
      setFormError("Choose a repository for this bundle.");
      return;
    }
    setFormError(null);
    setJSONValidationError(null);
    saveMutation.mutate({ mode: dialogMode, body, repositoryId: form.repositoryId });
  }

  return (
    <section className="space-y-4" aria-labelledby="preview-secrets-heading">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="space-y-1">
          <h2 id="preview-secrets-heading" className="text-sm font-semibold text-foreground">Preview secrets</h2>
          <p className="text-xs text-muted-foreground">Repo-scoped secret bundles used at preview runtime.</p>
        </div>
        <Button type="button" onClick={openCreateDialog} disabled={activeRepositories.length === 0}>
          <Plus className="h-4 w-4" />
          New bundle
        </Button>
      </div>

      <div className="max-w-md space-y-1.5">
        <Label htmlFor="preview-repository-select">Filter by repository</Label>
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
          onDelete={setDeleteTarget}
        />
      )}

      <BundleDialog
        mode={dialogMode}
        form={form}
        repositories={activeRepositories}
        repositoryName={selectedRepository?.full_name ?? ""}
        error={formError ?? jsonValidationError}
        saving={saveMutation.isPending}
        onOpenChange={(open) => {
          if (!open) closeBundleDialog();
        }}
        onFormChange={setForm}
        onRowChange={updateRow}
        onRowAdd={addRow}
        onRowRemove={removeRow}
        onReveal={(target) => revealMutation.mutate(target)}
        revealingTarget={revealMutation.isPending ? revealMutation.variables ?? null : null}
        revealedEnvRowIds={revealedEnvRowIds}
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
  onDelete,
}: {
  bundles: PreviewSecretBundleSummary[];
  isLoading: boolean;
  repositoryName: string;
  onCreate: () => void;
  onEdit: (bundle: PreviewSecretBundleSummary) => void;
  onDelete: (bundle: PreviewSecretBundleSummary) => void;
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
                  <BundleActions bundle={bundle} onEdit={onEdit} onDelete={onDelete} align="end" />
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
              <BundleActions bundle={bundle} onEdit={onEdit} onDelete={onDelete} />
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
  onDelete,
  align = "start",
}: {
  bundle: PreviewSecretBundleSummary;
  onEdit: (bundle: PreviewSecretBundleSummary) => void;
  onDelete: (bundle: PreviewSecretBundleSummary) => void;
  align?: "start" | "end";
}) {
  return (
    <div className={`flex flex-wrap gap-2${align === "end" ? " justify-end" : ""}`}>
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
  repositories,
  repositoryName,
  error,
  saving,
  onOpenChange,
  onFormChange,
  onRowChange,
  onRowAdd,
  onRowRemove,
  onReveal,
  revealingTarget,
  revealedEnvRowIds,
  onSubmit,
}: {
  mode: BundleDialogMode | null;
  form: BundleFormState;
  repositories: Repository[];
  repositoryName: string;
  error: string | null;
  saving: boolean;
  onOpenChange: (open: boolean) => void;
  onFormChange: (form: BundleFormState) => void;
  onRowChange: (index: number, patch: Partial<SecretValueRow>) => void;
  onRowAdd: () => void;
  onRowRemove: (index: number) => void;
  onReveal: (target: RevealTarget) => void;
  revealingTarget: RevealTarget | null;
  revealedEnvRowIds: Map<string, string>;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
}) {
  const isEdit = mode?.type === "edit";
  const editBundle = mode?.type === "edit" ? mode.bundle : null;
  const editHasFileOutputs = editBundle ? editBundle.outputs.some((o) => o.type === "file") : false;
  const existingEnvNames = new Set(editBundle ? envNamesFromBundle(editBundle) : []);
  const hasEnvOutput = form.rows.some((row) => {
    const key = row.key.trim();
    return key && (Boolean(row.value) || (isEdit && existingEnvNames.has(key)));
  });
  const wantsFileOutput = Boolean(form.filePath.trim()) || Boolean(form.fileContent);
  const canPreserveFileContent = isEdit && editHasFileOutputs && Boolean(form.filePath.trim()) && !form.fileContent;
  const hasFileOutput = Boolean(form.filePath.trim()) && (Boolean(form.fileContent) || canPreserveFileContent);
  const canSave = Boolean(form.repositoryId)
    && Boolean(form.name.trim())
    && (hasEnvOutput || hasFileOutput)
    && (!wantsFileOutput || hasFileOutput);
  const saveTooltip = form.fileContent && !form.filePath.trim()
    ? "Add the secret file path before saving"
    : form.filePath.trim() && !form.fileContent && !canPreserveFileContent
      ? "Paste the secret file contents before saving"
      : !hasEnvOutput && !hasFileOutput
        ? "Add at least one environment variable or secret file"
        : undefined;

  return (
    <Dialog open={Boolean(mode)} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{isEdit ? "Edit bundle" : "New bundle"}</DialogTitle>
          <DialogDescription>
            Secret values are not shown again after creation. Add one or more environment variables and optionally one generated file.
            {editHasFileOutputs
              ? " Leave the file contents blank to keep the encrypted file already stored, or paste new contents to replace it."
              : null}
          </DialogDescription>
        </DialogHeader>
        <form className="space-y-5" onSubmit={onSubmit}>
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor={isEdit ? "bundle-repository" : "bundle-repository-select"}>Bundle repository</Label>
              {isEdit ? (
                <Input id="bundle-repository" value={repositoryName} disabled />
              ) : (
                <Select
                  value={form.repositoryId}
                  onValueChange={(repositoryId) => onFormChange({ ...form, repositoryId })}
                  disabled={repositories.length === 0}
                >
                  <SelectTrigger id="bundle-repository-select">
                    <SelectValue placeholder="Choose repository" />
                  </SelectTrigger>
                  <SelectContent>
                    {repositories.map((repo) => (
                      <SelectItem key={repo.id} value={repo.id}>{repo.full_name}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
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

          <Tabs
            value={form.deliveryMode}
            onValueChange={(value) => onFormChange({ ...form, deliveryMode: value as BundleDeliveryMode })}
          >
            <TabsList aria-label="Bundle output editor" className="w-full sm:w-fit">
              <TabsTrigger value="env">Environment variables</TabsTrigger>
              <TabsTrigger value="file">Secret file</TabsTrigger>
            </TabsList>
            <TabsContent value="env" className="space-y-4">
              <StoredSecretsFields
                rows={form.rows}
                description="Each secret name becomes an environment variable in the preview runtime. Existing values can stay blank unless you want to replace them."
                canReveal={Boolean(editBundle)}
                revealBundle={editBundle}
                revealingTarget={revealingTarget}
                revealedEnvRowIds={revealedEnvRowIds}
                onRowChange={onRowChange}
                onRowAdd={onRowAdd}
                onRowRemove={onRowRemove}
                onReveal={onReveal}
              />
            </TabsContent>
            <TabsContent value="file" className="space-y-4">
              <SecretFileFields
                form={form}
                canReveal={isEdit && editHasFileOutputs}
                revealing={revealingTarget?.type === "file"}
                onReveal={() => editBundle && onReveal({ type: "file", bundle: editBundle })}
                onFormChange={onFormChange}
              />
            </TabsContent>
          </Tabs>

          {error ? <p className="text-sm text-destructive">{error}</p> : null}

          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline" disabled={saving}>Cancel</Button>
            </DialogClose>
            <SaveButton disabled={!canSave || saving} tooltip={saveTooltip}>
              <KeyRound className="h-4 w-4" />
              Save
            </SaveButton>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function SecretFileFields({
  form,
  canReveal,
  revealing,
  onReveal,
  onFormChange,
}: {
  form: BundleFormState;
  canReveal: boolean;
  revealing: boolean;
  onReveal: () => void;
  onFormChange: (form: BundleFormState) => void;
}) {
  const [isReplacingFileContent, setIsReplacingFileContent] = useState(false);
  const isMaskedFileContent = canReveal && !form.fileContent && !isReplacingFileContent;
  const contentValue = isMaskedFileContent ? MASKED_SECRET_FILE_PLACEHOLDER : form.fileContent;
  const contentPlaceholder = form.fileFormat === "json"
    ? '{\n  "token": "paste-secret-value-here"\n}'
    : "Paste the file contents here";

  return (
    <div className="space-y-4">
      <p className="text-xs text-muted-foreground">
        Paste the exact file that the preview app expects. 143 stores it encrypted and writes it into the preview workspace at runtime.
      </p>
      <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_12rem]">
        <div className="space-y-1.5">
          <Label htmlFor="secret-file-path">Secret file path</Label>
          <Input
            id="secret-file-path"
            value={form.filePath}
            onChange={(event) => onFormChange({ ...form, filePath: event.target.value })}
            placeholder="development.conf.json"
            autoComplete="off"
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="secret-file-type">Secret file type</Label>
          <Select
            value={form.fileFormat}
            onValueChange={(fileFormat) => onFormChange({ ...form, fileFormat: fileFormat as BundleFormState["fileFormat"] })}
          >
            <SelectTrigger id="secret-file-type">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="raw">Raw text</SelectItem>
              <SelectItem value="json">JSON</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>
      <div className="space-y-1.5">
        <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
          <Label htmlFor="secret-file-content">Secret file contents</Label>
          {canReveal ? (
            <Button
              type="button"
              variant="outline"
              size="icon"
              onClick={onReveal}
              disabled={revealing}
              aria-label="Reveal secret file contents"
              title="Reveal secret file contents"
            >
              <Eye className="h-4 w-4" />
            </Button>
          ) : null}
        </div>
        <Textarea
          id="secret-file-content"
          value={contentValue}
          onFocus={(event) => {
            if (isMaskedFileContent) {
              setIsReplacingFileContent(true);
              event.currentTarget.select();
            }
          }}
          onChange={(event) => onFormChange({
            ...form,
            fileContent: event.target.value,
          })}
          placeholder={contentPlaceholder}
          aria-label="Secret file contents"
          className={`min-h-40 font-mono text-xs${isMaskedFileContent ? " [-webkit-text-security:disc]" : ""}`}
          spellCheck={false}
        />
      </div>
    </div>
  );
}

function StoredSecretsFields({
  rows,
  description,
  canReveal,
  revealBundle,
  revealingTarget,
  revealedEnvRowIds,
  onRowChange,
  onRowAdd,
  onRowRemove,
  onReveal,
}: {
  rows: SecretValueRow[];
  description: string;
  canReveal: boolean;
  revealBundle: PreviewSecretBundleSummary | null;
  revealingTarget: RevealTarget | null;
  revealedEnvRowIds: Map<string, string>;
  onRowChange: (index: number, patch: Partial<SecretValueRow>) => void;
  onRowAdd: () => void;
  onRowRemove: (index: number) => void;
  onReveal: (target: RevealTarget) => void;
}) {
  const [replacingRowIds, setReplacingRowIds] = useState<Set<string>>(() => new Set());

  return (
    <div className="space-y-2">
      <div className="space-y-1">
        <div className="flex items-center gap-1.5">
          <Label>Stored secrets</Label>
          <HelpTooltip
            label="Stored secrets help"
            content="These are the encrypted values 143 stores for this bundle. The selected delivery method controls whether previews receive them as environment variables or inside a generated file."
          />
        </div>
        <p className="text-xs text-muted-foreground">{description}</p>
      </div>
      <div className="space-y-2">
        {rows.map((row, index) => {
          const key = row.key.trim();
          const canRevealRow = canReveal && Boolean(revealBundle) && Boolean(key);
          const isRevealed = revealedEnvRowIds.get(row.rowId) === key;
          const isMaskedValue = canRevealRow && !isRevealed && !row.value && !replacingRowIds.has(row.rowId);
          const value = isMaskedValue ? MASKED_SECRET_PLACEHOLDER : row.value;

          return (
            <div key={row.rowId} className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto_auto]">
              <Input
                value={row.key}
                onChange={(event) => onRowChange(index, { key: normalizeEnvKey(event.target.value) })}
                placeholder="API_TOKEN"
                aria-label={index === 0 ? "Secret name" : `Secret name ${index + 1}`}
                autoComplete="off"
              />
              <Input
                value={value}
                onFocus={(event) => {
                  if (isMaskedValue) {
                    setReplacingRowIds((current) => new Set(current).add(row.rowId));
                    event.currentTarget.select();
                  }
                }}
                onChange={(event) => onRowChange(index, {
                  value: event.target.value,
                })}
                placeholder="Secret value"
                type={isRevealed ? "text" : "password"}
                aria-label={index === 0 ? "Secret value" : `Secret value ${index + 1}`}
                autoComplete="new-password"
              />
              {canRevealRow ? (
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  onClick={() => onReveal({ type: "env", bundle: revealBundle!, rowId: row.rowId, key })}
                  disabled={Boolean(revealingTarget)}
                  aria-label={`Reveal secret value ${key}`}
                  title={`Reveal secret value ${key}`}
                >
                  <Eye className="h-4 w-4" />
                </Button>
              ) : <span />}
              <Button type="button" variant="outline" size="icon" onClick={() => onRowRemove(index)} aria-label={`Remove secret row ${index + 1}`}>
                <Trash2 className="h-4 w-4" />
              </Button>
            </div>
          );
        })}
      </div>
      <Button type="button" variant="outline" size="sm" onClick={onRowAdd}>
        <Plus className="h-4 w-4" />
        Add value
      </Button>
    </div>
  );
}

function SaveButton({ disabled, tooltip, children }: { disabled: boolean; tooltip?: string; children: ReactNode }) {
  const button = (
    <Button type="submit" disabled={disabled}>
      {children}
    </Button>
  );

  if (!disabled || !tooltip) {
    return button;
  }

  return (
    <TooltipProvider delayDuration={150}>
      <Tooltip>
        <TooltipTrigger asChild>
          <span className="inline-flex">{button}</span>
        </TooltipTrigger>
        <TooltipContent side="top" sideOffset={6}>
          {tooltip}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
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
          <h2 id="preview-api-heading" className="text-sm font-semibold text-foreground">Preview API tokens</h2>
          <p className="text-xs text-muted-foreground">Legacy preview-only tokens. Prefer external API clients for new session, automation, and preview integrations.</p>
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
          description="Use external API clients for new integrations; create preview-only tokens for older preview workflows."
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

function HelpTooltip({ label, content }: { label: string; content: ReactNode }) {
  return (
    <TooltipProvider delayDuration={150}>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button type="button" variant="ghost" size="icon" className="h-6 w-6 text-muted-foreground" aria-label={label}>
            <HelpCircle className="h-3.5 w-3.5" />
          </Button>
        </TooltipTrigger>
        <TooltipContent side="top" sideOffset={6} className="max-w-80">
          {content}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
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

function buildBundleRequest(
  form: BundleFormState,
  mode: BundleDialogMode,
): PreviewSecretBundlePatchRequest | PreviewSecretBundleUpsertRequest | Error {
  const name = form.name.trim();
  if (!name) {
    return new Error("Bundle name is required.");
  }

  const sourceValues: Record<string, string> = {};
  const outputs: PreviewSecretBundleOutput[] = [];
  const existingEnvNames = new Set(mode.type === "edit" ? envNamesFromBundle(mode.bundle) : []);
  const envValues: Record<string, string> = {};

  for (const row of form.rows) {
    const key = row.key.trim();
    if (!key && !row.value) continue;
    if (!key || (!row.value && (mode.type === "create" || !existingEnvNames.has(key)))) {
      return new Error("Each new secret value needs both a key and a value.");
    }
    envValues[key] = `secret:${key}`;
    if (row.value) {
      sourceValues[key] = row.value;
    }
  }
  if (Object.keys(envValues).length > 0) {
    outputs.push({ type: "env" as const, values: envValues });
  }

  const filePath = form.filePath.trim();
  const wantsFileOutput = Boolean(filePath) || Boolean(form.fileContent);
  if (wantsFileOutput) {
    if (!filePath) {
      return new Error("Secret file path is required.");
    }
    const fileOutput: PreviewSecretBundleOutput = {
      type: "file",
      path: filePath,
      format: form.fileFormat,
      value: `secret:${SECRET_FILE_KEY}`,
    };
    const canPreserveFileContent = mode.type === "edit"
      && fileOutputsFromBundle(mode.bundle).length > 0
      && !form.fileContent;
    if (!form.fileContent && !canPreserveFileContent) {
      return new Error("Secret file contents are required.");
    }
    if (form.fileContent) {
      if (form.fileFormat === "json") {
        try {
          JSON.parse(form.fileContent);
        } catch {
          return new Error(SECRET_FILE_JSON_ERROR);
        }
      }
      sourceValues[SECRET_FILE_KEY] = form.fileContent;
    }
    outputs.push(fileOutput);
  }

  if (outputs.length === 0) {
    return new Error("At least one environment variable or secret file is required.");
  }

  const body: PreviewSecretBundlePatchRequest | PreviewSecretBundleUpsertRequest = {
    name,
    outputs,
    exposure_policy: "preview_runtime",
  };
  if (Object.keys(sourceValues).length > 0 || mode.type === "create") {
    body.source = { type: "managed", values: sourceValues };
  }
  return body;
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

function getRevealedSecretValue(reveal: PreviewSecretBundleRevealResult, target: RevealTarget): string | null {
  const sourceKey = target.type === "file"
    ? getRevealedFileSourceKey(reveal)
    : getRevealedEnvSourceKey(reveal, target.key);
  if (!sourceKey) return null;
  return reveal.source.values[sourceKey] ?? null;
}

function getRevealedFileSourceKey(reveal: PreviewSecretBundleRevealResult): string | null {
  const fileOutputs = reveal.outputs.filter((output) => output.type === "file" && output.value?.startsWith("secret:"));
  if (fileOutputs.length !== 1) return null;
  return fileOutputs[0].value!.slice("secret:".length) || null;
}

function getRevealedEnvSourceKey(reveal: PreviewSecretBundleRevealResult, envName: string): string | null {
  for (const output of reveal.outputs) {
    const reference = output.type === "env" ? output.values?.[envName] : undefined;
    if (reference?.startsWith("secret:")) {
      return reference.slice("secret:".length) || null;
    }
  }
  return Object.hasOwn(reveal.source.values, envName) ? envName : null;
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
