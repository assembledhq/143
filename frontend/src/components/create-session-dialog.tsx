"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUp, Loader2, GitBranch, ChevronDown, Paperclip, ImagePlus, Plus } from "lucide-react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { PendingAttachmentStrip } from "@/components/pending-attachment-strip";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import { queryKeys } from "@/lib/query-keys";
import { AGENTS, agentTypeForModel } from "@/lib/agents";
import { useOptimisticSessionsSafe } from "@/contexts/optimistic-sessions";
import type { OrgSettings, Organization, Repository, SingleResponse, ListResponse } from "@/lib/types";

const MAX_FILE_SIZE = 10 * 1024 * 1024; // 10 MB

type BranchInfo = { name: string; protected: boolean };

interface CreateSessionDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function CreateSessionDialog({ open, onOpenChange }: CreateSessionDialogProps) {
  const router = useRouter();
  const queryClient = useQueryClient();
  const uploadInputRef = useRef<HTMLInputElement>(null);
  const messageInputRef = useRef<HTMLTextAreaElement>(null);
  // Synchronous guard: React Query's isPending flips on the next render, so
  // rapid Enter presses can all pass the isPending check in the same tick.
  const submittingRef = useRef(false);

  const [message, setMessage] = useState("");
  const [attachments, setAttachments] = useState<string[]>([]);
  const [isUploading, setIsUploading] = useState(false);
  const [showImageInput, setShowImageInput] = useState(false);
  const [imageURL, setImageURL] = useState("");
  const [selectedModel, setSelectedModel] = useState("");
  const [userSelectedRepoId, setUserSelectedRepoId] = useState<string | null>(null);
  const [branchByRepoId, setBranchByRepoId] = useState<Record<string, string>>({});
  const [creationError, setCreationError] = useState<string | null>(null);

  const { addOptimisticSession, removeOptimisticSession, markOptimisticResolved } = useOptimisticSessionsSafe();

  const { data: settingsResponse } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
    enabled: open,
  });

  const settings = settingsResponse?.data?.settings as OrgSettings | undefined;
  const defaultAgentType = settings?.default_agent_type ?? "codex";

  const { data: reposResponse } = useQuery<ListResponse<Repository>>({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
    enabled: open,
  });
  const repositories = useMemo(() => reposResponse?.data ?? [], [reposResponse]);

  const selectedRepoId = useMemo(() => {
    if (userSelectedRepoId !== null) return userSelectedRepoId;
    if (repositories.length === 1) return repositories[0].id;
    // Default to the last used repo if available
    if (repositories.length > 0) {
      try {
        const lastUsed = localStorage.getItem("143:lastUsedRepoId");
        if (lastUsed && repositories.some((r) => r.id === lastUsed)) return lastUsed;
      } catch {}
      return repositories[0].id;
    }
    return "";
  }, [userSelectedRepoId, repositories]);

  const selectedRepo = repositories.find((r) => r.id === selectedRepoId);

  const { data: branchesResponse, isLoading: branchesLoading, isError: branchesFailed } = useQuery<ListResponse<BranchInfo>>({
    queryKey: queryKeys.repositories.branches(selectedRepoId),
    queryFn: () => api.repositories.branches(selectedRepoId),
    enabled: open && !!selectedRepoId,
    staleTime: 5 * 60 * 1000,
  });
  const branches = useMemo(() => branchesResponse?.data ?? [], [branchesResponse]);

  const selectedBranch = useMemo(() => {
    if (!selectedRepoId) return "";
    if (branchByRepoId[selectedRepoId] !== undefined) return branchByRepoId[selectedRepoId];
    return selectedRepo?.default_branch ?? "";
  }, [selectedRepoId, branchByRepoId, selectedRepo]);

  const setSelectedBranch = (branch: string) => {
    if (!selectedRepoId) return;
    setBranchByRepoId((prev) => ({ ...prev, [selectedRepoId]: branch }));
  };

  const modelGroups = useMemo(() => {
    return [...AGENTS].sort((a, b) => {
      if (a.key === defaultAgentType) return -1;
      if (b.key === defaultAgentType) return 1;
      return AGENTS.indexOf(a) - AGENTS.indexOf(b);
    });
  }, [defaultAgentType]);

  // Reset state when dialog closes
  useEffect(() => {
    if (!open) {
      setMessage("");
      setAttachments([]);
      setIsUploading(false);
      setShowImageInput(false);
      setImageURL("");
      setSelectedModel("");
      setUserSelectedRepoId(null);
      setBranchByRepoId({});
      setCreationError(null);
      submittingRef.current = false;
    }
  }, [open]);

  // Focus input when dialog opens
  useEffect(() => {
    if (open) {
      setTimeout(() => messageInputRef.current?.focus(), 100);
    }
  }, [open]);

  const createMutation = useMutation({
    mutationFn: () =>
      api.sessions.createManual({
        message: message.trim(),
        images: attachments,
        ...(selectedModel ? { model: selectedModel, agent_type: agentTypeForModel(selectedModel) } : {}),
        ...(selectedRepoId ? { repository_id: selectedRepoId } : {}),
        ...(selectedBranch ? { branch: selectedBranch } : {}),
      }),
    onMutate: () => {
      setCreationError(null);
      const title = message.trim().length > 80
        ? message.trim().slice(0, 80) + "..."
        : message.trim();
      return { optimisticId: addOptimisticSession(title) };
    },
    onSuccess: (response, _variables, context) => {
      if (selectedRepoId) {
        try { localStorage.setItem("143:lastUsedRepoId", selectedRepoId); } catch {}
      }
      // Keep the optimistic row visible — the sidebar swaps it for the real
      // session once the refetch lands, so there's no double-render flash and
      // no empty gap. Cleanup happens there after the real row is observed.
      markOptimisticResolved(context.optimisticId, response.data.id);
      queryClient.invalidateQueries({ queryKey: queryKeys.sessions.all });
      onOpenChange(false);
      router.push(`/sessions/${response.data.id}`);
    },
    onError: (error, _variables, context) => {
      captureError(error, { feature: "session-create" });
      if (context?.optimisticId) {
        removeOptimisticSession(context.optimisticId);
      }
      setCreationError(
        error instanceof Error ? error.message : "Could not start session. Please try again.",
      );
      submittingRef.current = false;
    },
  });

  function submitCreateSession() {
    if (submittingRef.current) return;
    if (message.trim().length === 0) return;
    submittingRef.current = true;
    createMutation.mutate();
  }

  function resizeMessageInput() {
    const element = messageInputRef.current;
    if (!element) return;
    const maxHeight = 200;
    element.style.height = "auto";
    const nextHeight = Math.min(element.scrollHeight, maxHeight);
    element.style.height = `${nextHeight}px`;
    element.style.overflowY = element.scrollHeight > maxHeight ? "auto" : "hidden";
  }

  useEffect(() => {
    resizeMessageInput();
  }, [message]);

  async function onUploadChange(event: React.ChangeEvent<HTMLInputElement>) {
    const fileList = event.target.files;
    if (!fileList || fileList.length === 0) return;
    const files = Array.from(fileList);
    const oversized = files.filter((f) => f.size > MAX_FILE_SIZE);
    if (oversized.length > 0) {
      setCreationError(`File${oversized.length > 1 ? "s" : ""} too large (max 10 MB): ${oversized.map((f) => f.name).join(", ")}`);
      event.target.value = "";
      return;
    }
    setIsUploading(true);
    setCreationError(null);
    try {
      const results = await Promise.all(files.map((file) => api.uploads.upload(file)));
      setAttachments((prev) => [...prev, ...results.map((r) => r.url)]);
    } catch (err) {
      setCreationError(err instanceof Error ? err.message : "Upload failed");
    } finally {
      setIsUploading(false);
      event.target.value = "";
    }
  }

  function addImageURL() {
    const trimmed = imageURL.trim();
    if (!trimmed) return;
    setAttachments((prev) => [...prev, trimmed]);
    setImageURL("");
    setShowImageInput(false);
  }

  function removeAttachment(value: string) {
    setAttachments((prev) => prev.filter((item) => item !== value));
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[560px] p-0 gap-0 overflow-hidden" showCloseButton={false}>
        <DialogHeader className="px-5 pt-5 pb-3">
          <DialogTitle className="text-base font-semibold">New session</DialogTitle>
          <DialogDescription className="sr-only">Create a new coding agent session</DialogDescription>
        </DialogHeader>

        <div className="px-5 pb-2">
          <Textarea
            ref={messageInputRef}
            value={message}
            onChange={(e) => {
              setMessage(e.target.value);
              resizeMessageInput();
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                submitCreateSession();
              }
            }}
            placeholder="Tell the agent what to do..."
            rows={3}
            className="min-h-[80px] resize-none border-none bg-muted/40 rounded-lg px-3 py-2.5 text-sm shadow-none placeholder:text-muted-foreground/50 focus-visible:ring-1 focus-visible:ring-ring"
            aria-label="Session prompt"
          />
        </div>

        <PendingAttachmentStrip
          attachments={attachments}
          isUploading={isUploading}
          onRemove={removeAttachment}
          size="sm"
          className="px-5 pb-2"
        />

        {showImageInput && (
          <div className="flex items-center gap-2 px-5 pb-2">
            <Input
              value={imageURL}
              onChange={(e) => setImageURL(e.target.value)}
              placeholder="https://example.com/screenshot.png"
              aria-label="Image URL"
              className="text-sm"
            />
            <Button type="button" variant="outline" size="sm" onClick={addImageURL}>Add</Button>
          </div>
        )}

        <div className="flex items-center gap-1 px-4 py-3 border-t border-border bg-muted/30">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon" aria-label="Add files or photos" className="h-7 w-7 rounded-md text-muted-foreground hover:text-foreground">
                <Plus className="h-4 w-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start">
              <DropdownMenuItem onClick={() => uploadInputRef.current?.click()}>
                <Paperclip className="mr-2 h-4 w-4" />
                Upload files or photos
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => setShowImageInput(true)}>
                <ImagePlus className="mr-2 h-4 w-4" />
                Add image URL
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          <Input
            ref={uploadInputRef}
            type="file"
            accept="image/*,.pdf,.txt,.md,.json,.csv"
            multiple
            className="hidden"
            onChange={onUploadChange}
          />

          {repositories.length > 0 && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="sm" className="h-7 gap-1.5 rounded-md px-2 text-xs text-muted-foreground hover:text-foreground">
                  <GitBranch className="h-3 w-3" />
                  <span className="max-w-[100px] truncate">{selectedRepo ? selectedRepo.full_name.split("/").pop() : "Repo"}</span>
                  <ChevronDown className="h-2.5 w-2.5 opacity-50" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="w-72">
                {repositories.map((repo) => (
                  <DropdownMenuItem
                    key={repo.id}
                    onClick={() => setUserSelectedRepoId(repo.id)}
                    className={selectedRepoId === repo.id ? "font-medium" : ""}
                  >
                    <GitBranch className="mr-2 h-3.5 w-3.5 text-muted-foreground shrink-0" />
                    <span className="truncate">{repo.full_name}</span>
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          )}

          {selectedRepo && (
            branchesFailed ? (
              <div className="flex items-center gap-1">
                <GitBranch className="h-3 w-3 text-muted-foreground shrink-0" />
                <Input
                  value={selectedBranch}
                  onChange={(e) => setSelectedBranch(e.target.value)}
                  placeholder={selectedRepo.default_branch || "main"}
                  className="h-7 w-28 text-xs px-2"
                  aria-label="Target branch"
                />
              </div>
            ) : (
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="ghost" size="sm" className="h-7 gap-1.5 rounded-md px-2 text-xs text-muted-foreground hover:text-foreground" aria-label="Target branch">
                    <GitBranch className="h-3 w-3" />
                    <span className="max-w-[80px] truncate">{selectedBranch || selectedRepo.default_branch || "main"}</span>
                    <ChevronDown className="h-2.5 w-2.5 opacity-50" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start" className="w-64 max-h-72 overflow-y-auto">
                  {branchesLoading && <DropdownMenuItem disabled>Loading branches...</DropdownMenuItem>}
                  {!branchesLoading && branches.length === 0 && <DropdownMenuItem disabled>No branches found</DropdownMenuItem>}
                  {branches.map((branch) => (
                    <DropdownMenuItem
                      key={branch.name}
                      onClick={() => setSelectedBranch(branch.name)}
                      className={selectedBranch === branch.name ? "font-medium" : ""}
                    >
                      <GitBranch className="mr-2 h-3.5 w-3.5 text-muted-foreground shrink-0" />
                      <span className="truncate">{branch.name}</span>
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
            )
          )}

          <Select value={selectedModel} onValueChange={(v) => setSelectedModel(v === "__default__" ? "" : v)}>
            <SelectTrigger className="h-7 w-auto gap-1 border-none bg-transparent px-2 text-xs text-muted-foreground shadow-none hover:text-foreground focus:ring-0" aria-label="Model override">
              <SelectValue placeholder="Model" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__default__">Default model</SelectItem>
              {modelGroups.map((group) => (
                <SelectGroup key={group.key}>
                  <SelectLabel>{group.label}</SelectLabel>
                  {group.models.map((model) => (
                    <SelectItem key={model} value={model}>{model}</SelectItem>
                  ))}
                </SelectGroup>
              ))}
            </SelectContent>
          </Select>

          <div className="ml-auto">
            <Button
              type="button"
              size="sm"
              onClick={submitCreateSession}
              disabled={message.trim().length === 0 || createMutation.isPending}
              className="h-7 px-3 text-xs rounded-md"
            >
              {createMutation.isPending ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <>
                  <ArrowUp className="h-3.5 w-3.5 mr-1" />
                  Create
                </>
              )}
            </Button>
          </div>
        </div>

        {creationError && (
          <div className="px-5 pb-3">
            <p className="text-xs text-destructive">{creationError}</p>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
