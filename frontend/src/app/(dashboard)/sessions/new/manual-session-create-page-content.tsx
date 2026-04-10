"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUp, Mic, Plus, X, ImagePlus, Paperclip, GitBranch, ChevronDown, Loader2 } from "lucide-react";
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
import { api } from "@/lib/api";
import { isImageURL, fileNameFromURL } from "@/lib/utils";
import { captureError } from "@/lib/errors";
import { queryKeys } from "@/lib/query-keys";
import { AGENT_TYPE_OPTIONS, agentTypeForModel } from "@/lib/model-constants";
import { NoReposWarning } from "@/components/no-repos-warning";
import { AgentKeyRequiredBanner } from "@/components/agent-key-required-banner";
import { useOptimisticSessions } from "@/contexts/optimistic-sessions";
import type { OrgSettings, Organization, Repository, SingleResponse, ListResponse, ResolvedCredential } from "@/lib/types";

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

  // Read the currently selected repository from the URL query params
  // (set by the RepoContextSwitcher) so we clone the codebase into the sandbox.
  const repoId = searchParams.get("repo") ?? undefined;

  const [message, setMessage] = useState("");
  const [attachments, setAttachments] = useState<string[]>([]);
  const [isUploading, setIsUploading] = useState(false);
  const [showImageInput, setShowImageInput] = useState(false);
  const [imageURL, setImageURL] = useState("");
  const [isDictating, setIsDictating] = useState(false);
  const [dictationError, setDictationError] = useState<string | null>(null);
  const [selectedModel, setSelectedModel] = useState("");
  const [userSelectedRepoId, setUserSelectedRepoId] = useState<string | null>(repoId ?? null);
  const [branchByRepoId, setBranchByRepoId] = useState<Record<string, string>>({});
  const [creationError, setCreationError] = useState<string | null>(null);

  const { addOptimisticSession, removeOptimisticSession } = useOptimisticSessions();

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
    queryFn: () => api.credentials.listResolved(),
  });
  const resolvedCredentials = useMemo(() => resolvedCredsResponse?.data ?? [], [resolvedCredsResponse]);

  const { data: codexAuthResponse } = useQuery({
    queryKey: queryKeys.codexAuth.status,
    queryFn: () => api.codexAuth.status(),
  });

  // Auto-select the only repo for single-repo orgs; otherwise use user's choice.
  const selectedRepoId = useMemo(() => {
    if (userSelectedRepoId !== null) return userSelectedRepoId;
    if (repositories.length === 1) return repositories[0].id;
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

  const setSelectedRepoId = (id: string) => {
    setUserSelectedRepoId(id);
  };

  const setSelectedBranch = (branch: string) => {
    if (!selectedRepoId) return;
    setBranchByRepoId((prev) => ({ ...prev, [selectedRepoId]: branch }));
  };

  const modelGroups = useMemo(() => {
    // Sort so the default agent type appears first, preserve original order otherwise.
    return [...AGENT_TYPE_OPTIONS].sort((a, b) => {
      if (a.key === defaultAgentType) return -1;
      if (b.key === defaultAgentType) return 1;
      return AGENT_TYPE_OPTIONS.indexOf(a) - AGENT_TYPE_OPTIONS.indexOf(b);
    });
  }, [defaultAgentType]);

  // Determine which agent type would be used and whether credentials exist.
  const effectiveAgentType = selectedModel ? agentTypeForModel(selectedModel) ?? defaultAgentType : defaultAgentType;
  const AGENT_PROVIDER_MAP: Record<string, string> = {
    codex: "openai",
    claude_code: "anthropic",
    gemini_cli: "gemini",
  };
  const requiredProvider = AGENT_PROVIDER_MAP[effectiveAgentType] ?? "";
  const hasAgentCredentials =
    resolvedCredentials.some((c) => c.provider === requiredProvider)
    || (effectiveAgentType === "codex" && codexAuthResponse?.data?.status === "completed");

  const createManualSessionMutation = useMutation({
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
    onSuccess: async (response, _variables, context) => {
      await queryClient.invalidateQueries({ queryKey: queryKeys.sessions.all });
      removeOptimisticSession(context.optimisticId);
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
    },
  });

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
          <p className="mt-2 text-sm text-muted-foreground">Start a manual session with text, files, photos, or dictation.</p>
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
        <Card className="w-full max-w-3xl mx-auto border-border/60 bg-card shadow-lg rounded-2xl dark:shadow-[0_0_20px_oklch(0.6_0.15_270_/_6%)]">
          <CardContent className="space-y-0 p-4">
            <Textarea
              ref={messageInputRef}
              value={message}
              onChange={(event) => {
                setMessage(event.target.value);
                resizeMessageInput();
              }}
              onKeyDown={(event) => {
                if (event.key === "Enter" && !event.shiftKey) {
                  event.preventDefault();
                  if (message.trim().length > 0 && !createManualSessionMutation.isPending) {
                    createManualSessionMutation.mutate();
                  }
                }
              }}
              placeholder="Tell the agent what to do..."
              rows={1}
              className="min-h-[44px] resize-none border-none bg-transparent px-0 py-2 text-sm shadow-none placeholder:text-muted-foreground/60 focus-visible:ring-0"
              aria-label="Manual session prompt"
            />

            {(attachments.length > 0 || isUploading) && (
              <div className="flex flex-wrap items-center gap-2 pb-3">
                {attachments.map((url) => {
                  const isImage = isImageURL(url);
                  const fileName = url.startsWith("data:") ? "photo" : fileNameFromURL(url);
                  return (
                    <div key={url} className="relative group">
                      {isImage ? (
                        <img
                          src={url}
                          alt={fileName}
                          className="h-16 w-16 rounded-md object-cover border border-border"
                        />
                      ) : (
                        <Badge variant="secondary" className="gap-1 text-xs h-8">
                          {fileName}
                        </Badge>
                      )}
                      <button
                        type="button"
                        onClick={() => removeAttachment(url)}
                        className="absolute -top-1.5 -right-1.5 h-5 w-5 rounded-full bg-background border border-border flex items-center justify-center opacity-0 group-hover:opacity-100 transition-opacity"
                        aria-label={`Remove ${fileName}`}
                      >
                        <X className="h-3 w-3" />
                      </button>
                    </div>
                  );
                })}
                {isUploading && (
                  <div className="h-16 w-16 rounded-md border border-border bg-muted flex items-center justify-center">
                    <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
                  </div>
                )}
              </div>
            )}

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
                      className="h-8 gap-1.5 rounded-full px-3 text-[13px] text-muted-foreground hover:text-foreground"
                    >
                      <GitBranch className="h-3.5 w-3.5" />
                      <span>{selectedRepo ? selectedRepo.full_name.split("/").pop() : "Select repo"}</span>
                      <ChevronDown className="h-3 w-3 opacity-50" />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="start" className="w-72">
                    <DropdownMenuItem
                      onClick={() => setSelectedRepoId("")}
                      className={!selectedRepoId ? "font-medium" : ""}
                    >
                      No specific repo
                    </DropdownMenuItem>
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
                      className="h-7 w-36 text-[13px] px-2"
                      aria-label="Target branch"
                    />
                  </div>
                ) : (
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-8 gap-1.5 rounded-full px-3 text-[13px] text-muted-foreground hover:text-foreground"
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
                <SelectTrigger className="h-8 w-auto gap-1.5 border-none bg-transparent px-2 text-[13px] text-muted-foreground shadow-none hover:text-foreground focus:ring-0" aria-label="Model override">
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
                  onClick={() => createManualSessionMutation.mutate()}
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
  );
}
