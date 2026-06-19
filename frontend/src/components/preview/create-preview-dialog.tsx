"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ChevronDown, MonitorPlay, Play } from "lucide-react";

import { BranchPicker } from "@/components/branch-picker";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ErrorText } from "@/components/ui/error-notice";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { api } from "@/lib/api";
import type { BranchPreviewResponse, ListResponse, Repository } from "@/lib/types";

interface CreatePreviewDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  initialRepositoryId?: string;
  initialBranch?: string;
  onCreated?: (preview: BranchPreviewResponse) => void;
}

export function CreatePreviewDialog({
  open,
  onOpenChange,
  initialRepositoryId,
  initialBranch,
  onCreated,
}: CreatePreviewDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="gap-0 p-0 sm:max-w-xl">
        <DialogHeader className="border-b border-border px-5 py-4">
          <div className="flex items-center gap-2">
            <MonitorPlay className="h-4 w-4 text-muted-foreground" />
            <DialogTitle className="text-base">Create preview</DialogTitle>
          </div>
          <DialogDescription>
            Start a preview from a repository branch.
          </DialogDescription>
        </DialogHeader>
        {open ? (
          <CreatePreviewForm
            initialRepositoryId={initialRepositoryId}
            initialBranch={initialBranch}
            onCancel={() => onOpenChange(false)}
            onCreated={(preview) => {
              onCreated?.(preview);
              onOpenChange(false);
            }}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function CreatePreviewForm({
  initialRepositoryId,
  initialBranch,
  onCancel,
  onCreated,
}: {
  initialRepositoryId?: string;
  initialBranch?: string;
  onCancel: () => void;
  onCreated: (preview: BranchPreviewResponse) => void;
}) {
  const [repositoryId, setRepositoryId] = useState(initialRepositoryId ?? "");
  const [branch, setBranch] = useState(initialBranch ?? "");
  const [commitSha, setCommitSha] = useState("");
  const [previewConfigName, setPreviewConfigName] = useState("");
  const [ttlSeconds, setTTLSeconds] = useState("");
  const [repoSearch, setRepoSearch] = useState("");
  const [advancedOpen, setAdvancedOpen] = useState(false);

  const repositoriesQuery = useQuery<ListResponse<Repository>>({
    queryKey: ["repositories"],
    queryFn: () => api.repositories.list(),
  });

  const repositories = useMemo(() => repositoriesQuery.data?.data ?? [], [repositoriesQuery.data?.data]);
  const filteredRepositories = useMemo(
    () => repositories.filter((repo) => repo.full_name.toLowerCase().includes(repoSearch.trim().toLowerCase())),
    [repositories, repoSearch],
  );
  const selectedRepo = useMemo(() => {
    const explicit = repositories.find((repo) => repo.id === repositoryId);
    if (explicit) return explicit;
    if (!repositoryId && repositories.length === 1) return repositories[0];
    return undefined;
  }, [repositories, repositoryId]);
  const selectedRepositoryId = repositoryId || selectedRepo?.id || "";
  const selectedBranch = branch || selectedRepo?.default_branch || "";

  const configOptionsQuery = useQuery({
    queryKey: ["preview-config-options", selectedRepositoryId, selectedBranch, commitSha],
    queryFn: () =>
      api.previews.configOptions({
        repository_id: selectedRepositoryId,
        branch: selectedBranch,
        commit_sha: commitSha.trim() || undefined,
      }),
    enabled: selectedRepositoryId.length > 0 && selectedBranch.trim().length > 0,
  });
  const configOptions = configOptionsQuery.data?.data;
  const showConfigSelect =
    !!configOptions?.names.length &&
    (configOptions.requires_selection || configOptions.names.length > 1 || previewConfigName.length > 0);

  const createPreview = useMutation({
    mutationFn: () =>
      api.previews.create({
        repository_id: selectedRepositoryId,
        branch: selectedBranch,
        commit_sha: commitSha.trim() || undefined,
        preview_config_name: previewConfigName.trim() || configOptions?.selected_name || null,
        ttl_seconds: ttlSeconds.trim() ? Number(ttlSeconds) : undefined,
        source: { type: "manual" },
      }),
    onSuccess: (response) => onCreated(response.data),
  });

  const canStart =
    selectedRepositoryId.length > 0 &&
    selectedBranch.trim().length > 0 &&
    !configOptionsQuery.isLoading &&
    !configOptions?.validation_errors?.length &&
    !createPreview.isPending;

  function handleRepositoryChange(value: string) {
    setRepositoryId(value);
    setBranch("");
    setPreviewConfigName("");
  }

  return (
    <div className="space-y-5 px-5 py-5">
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor="preview-repository">Repository</Label>
          <Select value={selectedRepositoryId} onValueChange={handleRepositoryChange}>
            <SelectTrigger id="preview-repository" aria-label="Repository">
              {selectedRepo ? (
                <span className="truncate">{selectedRepo.full_name}</span>
              ) : (
                <SelectValue placeholder={repositoriesQuery.isLoading ? "Loading repositories..." : "Choose repository"} />
              )}
            </SelectTrigger>
            <SelectContent>
              <div className="p-2">
                <Input
                  value={repoSearch}
                  onChange={(event) => setRepoSearch(event.target.value)}
                  placeholder="Search repositories"
                />
              </div>
              {filteredRepositories.length ? (
                filteredRepositories.map((repo) => (
                  <SelectItem key={repo.id} value={repo.id}>
                    {repo.full_name}
                  </SelectItem>
                ))
              ) : (
                <div className="px-2 py-1.5 text-sm text-muted-foreground">No repositories match</div>
              )}
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-2">
          <Label htmlFor="preview-branch">Branch</Label>
          <BranchPicker
            repositoryId={selectedRepositoryId}
            value={selectedBranch}
            defaultBranch={selectedRepo?.default_branch}
            onValueChange={setBranch}
            label="Target branch"
            id="preview-branch"
            buttonClassName="w-full"
            contentClassName="max-h-[min(24rem,var(--radix-popover-content-available-height))]"
            disabled={!selectedRepositoryId}
          />
        </div>
      </div>

      {showConfigSelect ? (
        <div className="space-y-2">
          <Label htmlFor="preview-config-name">Preview config</Label>
          <Select value={previewConfigName || configOptions?.selected_name || ""} onValueChange={setPreviewConfigName}>
            <SelectTrigger id="preview-config-name">
              <SelectValue placeholder={configOptions.requires_selection ? "Choose config" : "Use default config"} />
            </SelectTrigger>
            <SelectContent>
              {configOptions.names.map((name) => (
                <SelectItem key={name} value={name}>
                  {name}
                  {name === configOptions.default_name ? " (default)" : ""}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      ) : null}

      <Collapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
        <CollapsibleTrigger asChild>
          <Button type="button" variant="outline" size="sm">
            <ChevronDown className="h-4 w-4" />
            Advanced
          </Button>
        </CollapsibleTrigger>
        <CollapsibleContent className="mt-4 grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="preview-commit">Commit SHA</Label>
            <Input
              id="preview-commit"
              value={commitSha}
              onChange={(event) => setCommitSha(event.target.value)}
              placeholder="Resolve branch head automatically"
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="preview-ttl">Lifetime seconds</Label>
            <Input
              id="preview-ttl"
              inputMode="numeric"
              value={ttlSeconds}
              onChange={(event) => setTTLSeconds(event.target.value.replace(/\D/g, ""))}
              placeholder="Use org default"
            />
          </div>
        </CollapsibleContent>
      </Collapsible>

      {selectedRepo ? (
        <p className="truncate text-sm text-muted-foreground">
          {selectedRepo.full_name}
          {selectedBranch ? ` · ${selectedBranch}` : ""}
        </p>
      ) : null}

      {configOptions?.validation_errors?.length ? (
        <ErrorText className="text-sm">{configOptions.validation_errors[0]}</ErrorText>
      ) : null}

      {createPreview.isError ? (
        <ErrorText className="text-sm">
          {createPreview.error instanceof Error ? createPreview.error.message : "Preview could not be created."}
        </ErrorText>
      ) : null}

      <DialogFooter>
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="button" onClick={() => createPreview.mutate()} disabled={!canStart}>
          <Play className="h-4 w-4" />
          Start preview
        </Button>
      </DialogFooter>
    </div>
  );
}
