"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUp, Mic, Plus, X, ImagePlus, Paperclip, GitBranch, ChevronDown } from "lucide-react";
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
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { AGENT_TYPE_OPTIONS } from "@/lib/model-constants";
import { useOptimisticSessions } from "@/contexts/optimistic-sessions";
import type { OrgSettings, Organization, Repository, SingleResponse, ListResponse } from "@/lib/types";

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

function readFileAsDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result ?? ""));
    reader.onerror = () => reject(new Error("file read failed"));
    reader.readAsDataURL(file);
  });
}

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

  // Auto-select the only repo for single-repo orgs; otherwise use user's choice.
  const selectedRepoId = useMemo(() => {
    if (userSelectedRepoId !== null) return userSelectedRepoId;
    if (repositories.length === 1) return repositories[0].id;
    return "";
  }, [userSelectedRepoId, repositories]);

  const selectedRepo = repositories.find((r) => r.id === selectedRepoId);

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

  const availableModels = useMemo(() => {
    const agentType = AGENT_TYPE_OPTIONS.find((a) => a.key === defaultAgentType);
    return agentType?.models ?? [];
  }, [defaultAgentType]);

  const createManualSessionMutation = useMutation({
    mutationFn: () =>
      api.sessions.createManual({
        message: message.trim(),
        images: attachments,
        ...(selectedModel ? { model: selectedModel } : {}),
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
    const uploadedAttachments = await Promise.all(files.map(async (file) => {
      if (file.type.startsWith("image/")) {
        return readFileAsDataURL(file);
      }
      return `attachment:${file.name}`;
    }));

    setAttachments((previous) => [...previous, ...uploadedAttachments]);
    event.target.value = "";
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
          <p className="text-3xl font-bold tracking-tight bg-[image:var(--gradient-primary)] bg-clip-text text-transparent">Let&apos;s build</p>
          <p className="mt-2 text-sm text-muted-foreground">Start a manual session with text, files, photos, or dictation.</p>
        </div>
      </div>

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
              className="min-h-[44px] resize-none border-none bg-transparent px-0 py-2 text-[15px] shadow-none placeholder:text-muted-foreground/60 focus-visible:ring-0"
              aria-label="Manual session prompt"
            />

            {attachments.length > 0 && (
              <div className="flex flex-wrap items-center gap-2 pb-3">
                {attachments.map((attachment) => (
                  <Badge key={attachment} variant="secondary" className="gap-1 text-xs">
                    {attachment.startsWith("data:") ? "photo" : attachment}
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      className="h-4 w-4 p-0"
                      onClick={() => removeAttachment(attachment)}
                      aria-label={`Remove ${attachment}`}
                    >
                      <X className="h-3 w-3" />
                    </Button>
                  </Badge>
                ))}
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
              )}

              <Select value={selectedModel} onValueChange={setSelectedModel}>
                <SelectTrigger className="h-8 w-auto gap-1.5 border-none bg-transparent px-2 text-[13px] text-muted-foreground shadow-none hover:text-foreground focus:ring-0" aria-label="Model override">
                  <SelectValue placeholder="Default model" />
                </SelectTrigger>
                <SelectContent>
                  {availableModels.map((model) => (
                    <SelectItem key={model} value={model}>
                      {model}
                    </SelectItem>
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
