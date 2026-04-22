"use client";

import { useDeferredValue, useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUp, Mic, Plus, ImagePlus, Paperclip, GitBranch, ChevronDown, FileCode2, FolderTree, X } from "lucide-react";
import { useRouter, useSearchParams } from "next/navigation";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
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
import { findActiveMention, insertMentionAtCaret, removeMentionReference, syncReferencesWithMessage } from "@/lib/session-composer-mentions";
import { queryKeys } from "@/lib/query-keys";
import {
  AGENTS,
  AGENTS_BY_KEY,
  agentTypeForModel,
  hasPiCredentials,
} from "@/lib/agents";
import { NoReposWarning } from "@/components/no-repos-warning";
import { AgentKeyRequiredBanner } from "@/components/agent-key-required-banner";
import { useOptimisticSessions } from "@/contexts/optimistic-sessions";
import type { OrgSettings, Organization, Repository, SingleResponse, ListResponse, ResolvedCredential, SessionInputReference } from "@/lib/types";

const MAX_FILE_SIZE = 10 * 1024 * 1024; // 10 MB

type BranchInfo = { name: string; protected: boolean };

type DictationResult = {
  transcript: string;
};

type DictationEvent = {
  results: DictationResult[][];
};

type BrowserSpeechRecognition = {
  continuous: boolean;
  interimResults: boolean;
  lang: string;
  onresult: ((event: DictationEvent) => void) | null;
  onerror: (() => void) | null;
  onend: (() => void) | null;
  start: () => void;
  stop: () => void;
};

type SpeechRecognitionCtor = new () => BrowserSpeechRecognition;

export function ManualSessionCreatePageContent() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const queryClient = useQueryClient();
  const uploadInputRef = useRef<HTMLInputElement>(null);
  const messageInputRef = useRef<HTMLTextAreaElement>(null);
  const recognitionRef = useRef<BrowserSpeechRecognition | null>(null);
  // Synchronous guard: React Query's isPending flips on the next render, so
  // rapid Enter presses can all pass the isPending check in the same tick.
  const submittingRef = useRef(false);

  // Read the currently selected repository from the URL query params
  // (set by the RepoContextSwitcher) so we clone the codebase into the sandbox.
  const repoId = searchParams.get("repo") ?? undefined;

  const [message, setMessage] = useState("");
  const [attachments, setAttachments] = useState<string[]>([]);
  const [references, setReferences] = useState<SessionInputReference[]>([]);
  const [isUploading, setIsUploading] = useState(false);
  const [showImageInput, setShowImageInput] = useState(false);
  const [imageURL, setImageURL] = useState("");
  const [isDictating, setIsDictating] = useState(false);
  const [dictationError, setDictationError] = useState<string | null>(null);
  const [selectedModel, setSelectedModel] = useState("");
  const [userSelectedRepoId, setUserSelectedRepoId] = useState<string | null>(repoId ?? null);
  const [branchByRepoId, setBranchByRepoId] = useState<Record<string, string>>({});
  const [creationError, setCreationError] = useState<string | null>(null);
  const [caretPosition, setCaretPosition] = useState(0);
  const [selectedMentionIndex, setSelectedMentionIndex] = useState(0);
  const [mentionDismissed, setMentionDismissed] = useState(false);
  const previousRepoIdRef = useRef<string>("");

  const { addOptimisticSession, removeOptimisticSession, markOptimisticResolved } = useOptimisticSessions();

  const { data: settingsResponse } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });

  const settings = settingsResponse?.data?.settings as OrgSettings | undefined;
  const defaultAgentType = settings?.default_agent_type ?? "codex";

  const { data: reposResponse } = useQuery<ListResponse<Repository>>({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
  });
  const repositories = useMemo(() => reposResponse?.data ?? [], [reposResponse]);

  const { data: resolvedCredsResponse } = useQuery<ListResponse<ResolvedCredential>>({
    queryKey: queryKeys.credentials.resolved,
    queryFn: () => api.userCredentials.listResolved(),
  });
  const resolvedCredentials = useMemo(() => resolvedCredsResponse?.data ?? [], [resolvedCredsResponse]);

  const { data: codexAuthResponse } = useQuery({
    queryKey: queryKeys.codexAuth.status,
    queryFn: () => api.codexAuth.status(),
  });

  // Auto-select: user's choice > last used repo > first repo.
  const selectedRepoId = useMemo(() => {
    if (userSelectedRepoId !== null) return userSelectedRepoId;
    if (repositories.length === 1) return repositories[0].id;
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
    enabled: !!selectedRepoId,
    staleTime: 5 * 60 * 1000,
  });
  const branches = useMemo(() => branchesResponse?.data ?? [], [branchesResponse]);

  // Derive branch: use user override if set, otherwise the repo's default.
  const selectedBranch = useMemo(() => {
    if (!selectedRepoId) return "";
    if (branchByRepoId[selectedRepoId] !== undefined) return branchByRepoId[selectedRepoId];
    return selectedRepo?.default_branch ?? "";
  }, [selectedRepoId, branchByRepoId, selectedRepo]);

  const activeMention = useMemo(
    () => findActiveMention(message, caretPosition),
    [message, caretPosition],
  );
  const deferredMentionQuery = useDeferredValue(activeMention?.query ?? "");
  const mentionQueryKey = `${selectedRepoId}:${selectedBranch}:${activeMention?.start ?? -1}:${activeMention?.query ?? ""}`;
  const { data: fileMentionsResponse, isFetching: fileMentionsLoading } = useQuery<ListResponse<SessionInputReference>>({
    queryKey: queryKeys.sessionComposer.files(selectedRepoId, selectedBranch, deferredMentionQuery),
    queryFn: () => api.sessionComposer.files(selectedRepoId, selectedBranch, deferredMentionQuery),
    enabled: !!selectedRepoId && activeMention !== null && !mentionDismissed,
    staleTime: 30 * 1000,
  });
  const fileMentions = useMemo(() => fileMentionsResponse?.data ?? [], [fileMentionsResponse]);
  const showMentionPicker = !!selectedRepoId && activeMention !== null && !mentionDismissed;

  const setSelectedRepoId = (id: string) => {
    setUserSelectedRepoId(id);
  };

  const setSelectedBranch = (branch: string) => {
    if (!selectedRepoId) return;
    setBranchByRepoId((prev) => ({ ...prev, [selectedRepoId]: branch }));
  };

  const modelGroups = useMemo(() => {
    // Sort so the default agent type appears first, preserve original order otherwise.
    return [...AGENTS].sort((a, b) => {
      if (a.key === defaultAgentType) return -1;
      if (b.key === defaultAgentType) return 1;
      return AGENTS.indexOf(a) - AGENTS.indexOf(b);
    });
  }, [defaultAgentType]);

  // Determine which agent type would be used and whether credentials exist.
  const effectiveAgentType = selectedModel ? agentTypeForModel(selectedModel) ?? defaultAgentType : defaultAgentType;
  const requiredProvider = AGENTS_BY_KEY[effectiveAgentType]?.providerKey ?? "";
  // Pi routes to Anthropic/OpenAI/Gemini depending on the *selected model*. For
  // curated prefixes we mirror checkPiProviderKey's per-model lookup so the
  // banner matches what the orchestrator will accept. When no model has been
  // picked we mirror piResolvedModel's hardcoded fallback (Claude Opus 4.7 →
  // Anthropic) so the UI doesn't show "Ready to run" for a run that would hit
  // "missing ANTHROPIC_API_KEY" from the backend.
  const hasAgentCredentials =
    effectiveAgentType === "pi"
      ? hasPiCredentials(resolvedCredentials, selectedModel)
      : resolvedCredentials.some((c) => c.provider === requiredProvider)
        || (effectiveAgentType === "codex" && codexAuthResponse?.data?.status === "completed");

  const createManualSessionMutation = useMutation({
    mutationFn: () =>
      api.sessions.createManual({
        message: message.trim(),
        images: attachments,
        references,
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
      // session once the refetch lands. See OptimisticSession.resolvedId.
      markOptimisticResolved(context.optimisticId, response.data.id);
      queryClient.invalidateQueries({ queryKey: queryKeys.sessions.all });
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

  function submitManualSession() {
    if (submittingRef.current) return;
    if (message.trim().length === 0) return;
    submittingRef.current = true;
    createManualSessionMutation.mutate();
  }

  function resizeMessageInput() {
    const element = messageInputRef.current;
    if (!element) {
      return;
    }

    const maxHeight = 240;
    element.style.height = "auto";
    const nextHeight = Math.min(element.scrollHeight, maxHeight);
    element.style.height = `${nextHeight}px`;
    element.style.overflowY = element.scrollHeight > maxHeight ? "auto" : "hidden";
  }

  useEffect(() => {
    resizeMessageInput();
  }, [message]);

  useEffect(() => {
    if (!messageInputRef.current || document.activeElement !== messageInputRef.current) {
      return;
    }
    setCaretPosition(messageInputRef.current.selectionStart ?? message.length);
  }, [message]);

  useEffect(() => {
    setMentionDismissed(false);
    setSelectedMentionIndex(0);
  }, [mentionQueryKey]);

  useEffect(() => {
    const previousRepoID = previousRepoIdRef.current;
    if (!previousRepoID) {
      previousRepoIdRef.current = selectedRepoId;
      return;
    }
    if (previousRepoID === selectedRepoId) {
      return;
    }
    previousRepoIdRef.current = selectedRepoId;
    setMessage((previous) => references.reduce((next, reference) => removeMentionReference(next, reference), previous));
    setReferences([]);
  }, [references, selectedRepoId]);

  function updateMessage(nextMessage: string, nextCaret: number) {
    setMessage(nextMessage);
    setReferences((previous) => syncReferencesWithMessage(nextMessage, previous));
    setCaretPosition(nextCaret);
  }

  function applyMention(reference: SessionInputReference) {
    if (!activeMention || !messageInputRef.current) {
      return;
    }

    const inserted = insertMentionAtCaret(message, activeMention, reference);
    setMessage(inserted.text);
    setReferences((previous) => {
      const existing = previous.find((item) => (item.token ?? item.display) === (reference.token ?? reference.display));
      if (existing) {
        return syncReferencesWithMessage(inserted.text, previous);
      }
      return syncReferencesWithMessage(inserted.text, [...previous, reference]);
    });
    setCaretPosition(inserted.caret);
    setMentionDismissed(false);

    requestAnimationFrame(() => {
      if (!messageInputRef.current) {
        return;
      }
      messageInputRef.current.focus();
      messageInputRef.current.setSelectionRange(inserted.caret, inserted.caret);
    });
  }

  function removeReference(reference: SessionInputReference) {
    const nextMessage = removeMentionReference(message, reference);
    setMessage(nextMessage);
    setReferences((previous) => previous.filter((item) => (item.token ?? item.display) !== (reference.token ?? reference.display)));
    setCaretPosition(nextMessage.length);
  }

  async function onUploadChange(event: React.ChangeEvent<HTMLInputElement>) {
    const fileList = event.target.files;
    if (!fileList || fileList.length === 0) {
      return;
    }

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
      const results = await Promise.all(
        files.map((file) => api.uploads.upload(file))
      );
      setAttachments((previous) => [...previous, ...results.map((r) => r.url)]);
    } catch (err) {
      setCreationError(err instanceof Error ? err.message : "Upload failed");
    } finally {
      setIsUploading(false);
      event.target.value = "";
    }
  }

  function addImageURL() {
    const trimmed = imageURL.trim();
    if (!trimmed) {
      return;
    }
    setAttachments((previous) => [...previous, trimmed]);
    setImageURL("");
    setShowImageInput(false);
  }

  function removeAttachment(value: string) {
    setAttachments((previous) => previous.filter((item) => item !== value));
  }

  function getSpeechRecognitionCtor(): SpeechRecognitionCtor | null {
    const browserWindow = window as Window & {
      SpeechRecognition?: SpeechRecognitionCtor;
      webkitSpeechRecognition?: SpeechRecognitionCtor;
    };
    return browserWindow.SpeechRecognition || browserWindow.webkitSpeechRecognition || null;
  }

  function toggleDictation() {
    setDictationError(null);

    if (isDictating && recognitionRef.current) {
      recognitionRef.current.stop();
      return;
    }

    const Ctor = getSpeechRecognitionCtor();
    if (!Ctor) {
      setDictationError("Dictation is not supported in this browser.");
      return;
    }

    const recognition = new Ctor();
    recognition.continuous = false;
    recognition.interimResults = false;
    recognition.lang = "en-US";
    recognition.onresult = (event) => {
      const transcript = event.results?.[0]?.[0]?.transcript?.trim();
      if (!transcript) {
        return;
      }
      setMessage((previous) => (previous ? `${previous} ${transcript}` : transcript));
    };
    recognition.onerror = () => {
      setDictationError("Dictation failed. Please type your request.");
    };
    recognition.onend = () => {
      setIsDictating(false);
      recognitionRef.current = null;
    };

    recognitionRef.current = recognition;
    setIsDictating(true);
    recognition.start();
  }

  return (
    <div className="flex flex-col h-full">
      {/* Centered hero + composer */}
      <div className="flex-1 flex flex-col items-center justify-center px-6 pb-4">
        <div className="text-center mb-8">
          <p className="text-3xl font-semibold tracking-tight bg-[image:var(--gradient-primary)] bg-clip-text text-transparent">Let&apos;s build</p>
          <p className="mt-2 text-xs text-muted-foreground">Start a manual session with text, files, photos, or dictation.</p>
        </div>
      </div>

      {/* No repos warning */}
      {repositories.length === 0 && (
        <div className="shrink-0 px-4">
          <div className="w-full max-w-3xl mx-auto">
            <NoReposWarning />
          </div>
        </div>
      )}

      {/* Agent credentials warning */}
      {!hasAgentCredentials && (
        <div className="shrink-0 px-4">
          <div className="w-full max-w-3xl mx-auto">
            <AgentKeyRequiredBanner agentType={effectiveAgentType} />
          </div>
        </div>
      )}

      {/* Composer pinned to bottom */}
      <div className="shrink-0 px-4 pb-4">
        <div className="relative w-full max-w-3xl mx-auto">
          {showMentionPicker && (
            <Card
              data-testid="mention-picker"
              className="absolute inset-x-0 bottom-full z-20 mb-2 overflow-hidden border-border/70 bg-background/95 shadow-lg backdrop-blur-sm"
            >
              <CardContent className="p-2">
                <div className="mb-2 text-xs font-medium uppercase tracking-[0.14em] text-muted-foreground">
                  Files and directories
                </div>
                {fileMentionsLoading && (
                  <p className="px-2 py-1 text-xs text-muted-foreground">Loading matches…</p>
                )}
                {!fileMentionsLoading && fileMentions.length === 0 && (
                  <p className="px-2 py-1 text-xs text-muted-foreground">No matches for @{activeMention?.query}</p>
                )}
                {!fileMentionsLoading && fileMentions.length > 0 && (
                  <div className="max-h-[min(20rem,calc(100vh-16rem))] space-y-1 overflow-y-auto">
                    {fileMentions.map((reference, index) => (
                      <Button
                        key={`${reference.kind}:${reference.path ?? reference.id ?? reference.display}`}
                        type="button"
                        variant="ghost"
                        className={`flex h-auto w-full items-center justify-start gap-2 rounded-lg px-2 py-2 text-left ${index === selectedMentionIndex ? "bg-accent text-accent-foreground" : ""}`}
                        onMouseDown={(event) => event.preventDefault()}
                        onClick={() => applyMention(reference)}
                      >
                        {reference.kind === "directory" ? <FolderTree className="h-4 w-4 shrink-0" /> : <FileCode2 className="h-4 w-4 shrink-0" />}
                        <span className="truncate text-xs">{reference.display}</span>
                      </Button>
                    ))}
                  </div>
                )}
              </CardContent>
            </Card>
          )}

          <Card className="w-full rounded-2xl border-border/60 bg-card shadow-lg dark:shadow-[0_0_20px_oklch(0.6_0.15_270_/_6%)]">
            <CardContent className="space-y-0 p-4">
              <Textarea
                ref={messageInputRef}
                value={message}
                onChange={(event) => {
                  updateMessage(event.target.value, event.target.selectionStart ?? event.target.value.length);
                  resizeMessageInput();
                }}
                onClick={(event) => setCaretPosition(event.currentTarget.selectionStart ?? message.length)}
                onKeyUp={(event) => setCaretPosition(event.currentTarget.selectionStart ?? message.length)}
                onSelect={(event) => setCaretPosition(event.currentTarget.selectionStart ?? message.length)}
                onKeyDown={(event) => {
                  if (showMentionPicker && fileMentions.length > 0) {
                    if (event.key === "ArrowDown") {
                      event.preventDefault();
                      setSelectedMentionIndex((previous) => (previous + 1) % fileMentions.length);
                      return;
                    }
                    if (event.key === "ArrowUp") {
                      event.preventDefault();
                      setSelectedMentionIndex((previous) => (previous - 1 + fileMentions.length) % fileMentions.length);
                      return;
                    }
                    if (event.key === "Enter" && !event.shiftKey) {
                      event.preventDefault();
                      applyMention(fileMentions[selectedMentionIndex]);
                      return;
                    }
                  }
                  if (showMentionPicker && event.key === "Escape") {
                    event.preventDefault();
                    setMentionDismissed(true);
                    return;
                  }
                  if (event.key === "Enter" && !event.shiftKey) {
                    event.preventDefault();
                    submitManualSession();
                  }
                }}
                placeholder="Tell the agent what to do..."
                rows={1}
                disabled={createManualSessionMutation.isPending}
                className="min-h-[44px] resize-none border-none bg-transparent px-0 py-2 text-xs shadow-none placeholder:text-muted-foreground/60 focus-visible:ring-0 disabled:opacity-60 disabled:cursor-not-allowed"
                aria-label="Manual session prompt"
              />

              {references.length > 0 && (
                <div className="flex flex-wrap gap-2 pb-3" aria-label="Selected references">
                  {references.map((reference) => (
                    <Badge
                      key={`${reference.kind}:${reference.path ?? reference.id ?? reference.display}`}
                      variant="secondary"
                      className="gap-1 rounded-full border-border/60 bg-muted/60 pl-2 pr-1"
                    >
                      {reference.kind === "directory" ? <FolderTree className="h-3 w-3" /> : <FileCode2 className="h-3 w-3" />}
                      <span className="max-w-[18rem] truncate">{reference.display}</span>
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        className="h-5 w-5 rounded-full"
                        aria-label={`Remove ${reference.display}`}
                        onClick={() => removeReference(reference)}
                      >
                        <X className="h-3 w-3" />
                      </Button>
                    </Badge>
                  ))}
                </div>
              )}

              <PendingAttachmentStrip
                attachments={attachments}
                isUploading={isUploading}
                onRemove={removeAttachment}
                size="md"
                className="pb-3"
              />

              {showImageInput && (
                <div className="flex items-center gap-2 pb-3">
                  <Input
                    value={imageURL}
                    onChange={(event) => setImageURL(event.target.value)}
                    placeholder="https://example.com/screenshot.png"
                    aria-label="Image URL"
                  />
                  <Button type="button" variant="outline" onClick={addImageURL}>
                    Add
                  </Button>
                </div>
              )}

              <div className="flex items-center gap-1 pt-2">
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button variant="ghost" size="icon" aria-label="Add files or photos" className="h-8 w-8 rounded-full text-muted-foreground hover:text-foreground">
                      <Plus className="h-5 w-5" />
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
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-8 gap-1.5 rounded-full px-3 text-xs text-muted-foreground hover:text-foreground"
                      >
                        <GitBranch className="h-3.5 w-3.5" />
                        <span>{selectedRepo ? selectedRepo.full_name.split("/").pop() : "Select repo"}</span>
                        <ChevronDown className="h-3 w-3 opacity-50" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="start" className="w-72">
                      {repositories.map((repo) => (
                        <DropdownMenuItem
                          key={repo.id}
                          onClick={() => setSelectedRepoId(repo.id)}
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
                      <GitBranch className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
                      <Input
                        value={selectedBranch}
                        onChange={(e) => setSelectedBranch(e.target.value)}
                        placeholder={selectedRepo.default_branch || "main"}
                        className="h-7 w-36 text-xs px-2"
                        aria-label="Target branch"
                      />
                    </div>
                  ) : (
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button
                          variant="ghost"
                          size="sm"
                          className="h-8 gap-1.5 rounded-full px-3 text-xs text-muted-foreground hover:text-foreground"
                          aria-label="Target branch"
                        >
                          <GitBranch className="h-3.5 w-3.5" />
                          <span>{selectedBranch || selectedRepo.default_branch || "main"}</span>
                          <ChevronDown className="h-3 w-3 opacity-50" />
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="start" className="w-64 max-h-72 overflow-y-auto">
                        {branchesLoading && (
                          <DropdownMenuItem disabled>Loading branches…</DropdownMenuItem>
                        )}
                        {!branchesLoading && branches.length === 0 && (
                          <DropdownMenuItem disabled>No branches found</DropdownMenuItem>
                        )}
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
                  <SelectTrigger className="h-8 w-auto gap-1.5 border-none bg-transparent px-2 text-xs text-muted-foreground shadow-none hover:text-foreground focus:ring-0" aria-label="Model override">
                    <SelectValue placeholder="Default model" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="__default__">Default model</SelectItem>
                    {modelGroups.map((group) => (
                      <SelectGroup key={group.key}>
                        <SelectLabel>{group.label}</SelectLabel>
                        {group.models.map((model) => (
                          <SelectItem key={model} value={model}>
                            {model}
                          </SelectItem>
                        ))}
                      </SelectGroup>
                    ))}
                  </SelectContent>
                </Select>

                <div className="ml-auto flex items-center gap-1">
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    onClick={toggleDictation}
                    className="h-8 w-8 rounded-full text-muted-foreground hover:text-foreground"
                    aria-label="Dictate"
                  >
                    <Mic className={`h-[18px] w-[18px] ${isDictating ? "text-primary" : ""}`} />
                  </Button>
                  <Button
                    type="button"
                    size="icon"
                    onClick={submitManualSession}
                    disabled={message.trim().length === 0 || createManualSessionMutation.isPending}
                    className="h-8 w-8 rounded-full"
                    aria-label="Start session"
                  >
                    <ArrowUp className="h-4 w-4" />
                  </Button>
                </div>
              </div>

              {dictationError && (
                <p className="pt-2 text-xs text-destructive">{dictationError}</p>
              )}
              {creationError && (
                <p className="pt-2 text-xs text-destructive">{creationError}</p>
              )}
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  );
}
