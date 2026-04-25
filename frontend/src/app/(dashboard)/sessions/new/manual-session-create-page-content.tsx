"use client";

import { useDeferredValue, useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUp, Mic, Plus, ImagePlus, Paperclip, GitBranch, ChevronDown, FileCode2, FolderTree, Slash, X } from "lucide-react";
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
import { BranchPicker } from "@/components/branch-picker";
import { PendingAttachmentStrip } from "@/components/pending-attachment-strip";
import { SessionComposerTriggerPicker, flattenGroups, type TriggerPickerGroup, type TriggerPickerPosition } from "@/components/session-composer-trigger-picker";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import {
  COMPOSER_TRIGGER_SPECS,
  findActiveTrigger,
  insertCommandAtCaret,
  insertMentionAtCaret,
  removeCommandReference,
  removeMentionReference,
  syncCommandsWithMessage,
  syncReferencesWithMessage,
} from "@/lib/session-composer-mentions";
import { useSessionComposerSlashCommands } from "@/hooks/use-session-composer-slash-commands";
import { clearDraft, loadDraft, saveDraft } from "@/lib/session-draft";
import { queryKeys } from "@/lib/query-keys";
import { cn } from "@/lib/utils";
import {
  AGENTS,
  AGENTS_BY_KEY,
  agentTypeForModel,
} from "@/lib/agents";
import { NoReposWarning } from "@/components/no-repos-warning";
import { AgentKeyRequiredBanner } from "@/components/agent-key-required-banner";
import { useOptimisticSessions } from "@/contexts/optimistic-sessions";
import { useAuth } from "@/hooks/use-auth";
import {
  type CodingAgentReasoningEffort,
  getDefaultCodingAgentReasoningForAgent,
  getCodingAgentReasoningOptions,
  isCodingAgentReasoningEffortSupported,
  supportsReasoningEffort,
  toCodingAgentReasoningEffort,
} from "@/lib/coding-agent-reasoning";
import type { OrgSettings, Organization, Repository, SingleResponse, ListResponse, ResolvedCredential, SessionInputCommand, SessionInputReference } from "@/lib/types";

const MAX_FILE_SIZE = 10 * 1024 * 1024; // 10 MB

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

type MentionPickerPosition = TriggerPickerPosition;

export function ManualSessionCreatePageContent() {
  const { user } = useAuth();
  const router = useRouter();
  const searchParams = useSearchParams();
  const queryClient = useQueryClient();
  const uploadInputRef = useRef<HTMLInputElement>(null);
  const messageInputRef = useRef<HTMLTextAreaElement>(null);
  const composerCardRef = useRef<HTMLDivElement>(null);
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
  const [commands, setCommands] = useState<SessionInputCommand[]>([]);
  const [isUploading, setIsUploading] = useState(false);
  const [showImageInput, setShowImageInput] = useState(false);
  const [imageURL, setImageURL] = useState("");
  const [isDictating, setIsDictating] = useState(false);
  const [dictationError, setDictationError] = useState<string | null>(null);
  const [selectedModel, setSelectedModel] = useState("");
  const [reasoningOverride, setReasoningOverride] = useState<CodingAgentReasoningEffort>("");
  const [userSelectedRepoId, setUserSelectedRepoId] = useState<string | null>(repoId ?? null);
  const [branchByRepoId, setBranchByRepoId] = useState<Record<string, string>>({});
  const [creationError, setCreationError] = useState<string | null>(null);
  const [caretPosition, setCaretPosition] = useState(0);
  const [selectedMentionIndex, setSelectedMentionIndex] = useState(0);
  const [mentionDismissed, setMentionDismissed] = useState(false);
  const [mentionPickerPosition, setMentionPickerPosition] = useState<MentionPickerPosition | null>(null);
  const [isDragActive, setIsDragActive] = useState(false);
  const [dragMessage, setDragMessage] = useState<string | null>(null);
  // Gates the persist effect until after hydration so we never overwrite a
  // stored draft with the component's initial (empty) state on first mount.
  const [draftHydrated, setDraftHydrated] = useState(false);
  const previousRepoIdRef = useRef<string>("");
  const dragDepthRef = useRef(0);

  const { addOptimisticSession, removeOptimisticSession, markOptimisticResolved } = useOptimisticSessions();

  function projectCommandsOnly(items: SessionInputCommand[]): SessionInputCommand[] {
    return items.filter((command) => command.source === "project");
  }

  function removeCommandsFromMessage(text: string, items: SessionInputCommand[]): string {
    return items.reduce(
      (next, command) => removeCommandReference(next, command),
      text,
    );
  }

  // Hydrate once on mount. A `?repo=` URL param represents fresh explicit
  // intent (e.g. the user just clicked a repo in the switcher), so it wins
  // over the repo stored in the draft. When the URL's repo conflicts with
  // the draft's, we keep repo-agnostic fields (prompt, attachments, model)
  // but drop references/mentions — they were resolved against a different
  // repo tree and wouldn't match the new one.
  useEffect(() => {
    const draft = loadDraft();
    if (draft) {
      const repoConflict =
        !!repoId && !!draft.userSelectedRepoId && repoId !== draft.userSelectedRepoId;

      setAttachments(draft.attachments);
      setSelectedModel(draft.selectedModel);
      setReasoningOverride(draft.reasoningOverride);
      setShowImageInput(draft.showImageInput);
      setImageURL(draft.imageURL);
      setBranchByRepoId(draft.branchByRepoId);
      if (!repoId) {
        setUserSelectedRepoId(draft.userSelectedRepoId);
      }

      const draftProjectCommands = projectCommandsOnly(draft.commands);
      if (repoConflict) {
        const strippedReferences = draft.references.reduce(
          (text, reference) => removeMentionReference(text, reference),
          draft.message,
        );
        setMessage(removeCommandsFromMessage(strippedReferences, draftProjectCommands));
        setReferences([]);
        setCommands(draft.commands.filter((command) => command.source !== "project"));
      } else {
        setMessage(draft.message);
        setReferences(draft.references);
        setCommands(draft.commands);
      }

      // Put the caret at the end so the user can keep typing where they left
      // off rather than having to click into the field.
      requestAnimationFrame(() => {
        const el = messageInputRef.current;
        if (!el) return;
        const end = el.value.length;
        el.setSelectionRange(end, end);
        setCaretPosition(end);
      });
    }
    setDraftHydrated(true);
    // Intentionally one-shot: we only restore at the moment the composer
    // mounts. Subsequent URL or state changes should not re-hydrate.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Persist the serializable slice of form state on every change. Pure UI
  // state (caret, dictation, mention picker, errors, upload-in-flight flag)
  // is deliberately excluded — restoring it on reload would be meaningless or
  // confusing.
  useEffect(() => {
    if (!draftHydrated) return;
    saveDraft({
      message,
      attachments,
      references,
      commands,
      selectedModel,
      reasoningOverride,
      userSelectedRepoId,
      branchByRepoId,
      showImageInput,
      imageURL,
    });
  }, [
    draftHydrated,
    message,
    attachments,
    references,
    commands,
    selectedModel,
    reasoningOverride,
    userSelectedRepoId,
    branchByRepoId,
    showImageInput,
    imageURL,
  ]);

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

  // Drop a hydrated repo id (from the draft or the `?repo=` URL param) if the
  // repos query has resolved and the id isn't in the list — repo was deleted,
  // access was revoked, or the URL was bogus. Without this, the picker would
  // silently show "Select repo" while state still held the dead id, and the
  // dead id would keep getting re-persisted into the draft. We only act once
  // the query actually resolves (reposResponse truthy) so that transient
  // loading/error states don't nuke a valid selection.
  useEffect(() => {
    if (userSelectedRepoId === null) return;
    if (!reposResponse) return;
    if (!repositories.some((r) => r.id === userSelectedRepoId)) {
      setUserSelectedRepoId(null);
    }
  }, [userSelectedRepoId, reposResponse, repositories]);

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
  // Derive branch: use user override if set, otherwise the repo's default.
  const selectedBranch = useMemo(() => {
    if (!selectedRepoId) return "";
    if (branchByRepoId[selectedRepoId] !== undefined) return branchByRepoId[selectedRepoId];
    return selectedRepo?.default_branch ?? "";
  }, [selectedRepoId, branchByRepoId, selectedRepo]);

  const activeTrigger = useMemo(
    () => findActiveTrigger(message, caretPosition, COMPOSER_TRIGGER_SPECS),
    [message, caretPosition],
  );
  const activeMention = activeTrigger?.trigger === "@" ? activeTrigger : null;
  const activeCommand = activeTrigger?.trigger === "/" ? activeTrigger : null;
  const deferredMentionQuery = useDeferredValue(activeMention?.query ?? "");
  const deferredCommandQuery = useDeferredValue(activeCommand?.query ?? "");
  const triggerQueryKey = `${activeTrigger?.trigger ?? ""}:${selectedRepoId}:${selectedBranch}:${activeTrigger?.start ?? -1}:${activeTrigger?.query ?? ""}`;
  const { data: fileMentionsResponse, isFetching: fileMentionsLoading } = useQuery<ListResponse<SessionInputReference>>({
    queryKey: queryKeys.sessionComposer.files(selectedRepoId, selectedBranch, deferredMentionQuery),
    queryFn: () => api.sessionComposer.files(selectedRepoId, selectedBranch, deferredMentionQuery),
    enabled: !!selectedRepoId && activeMention !== null && !mentionDismissed,
    staleTime: 30 * 1000,
  });
  const fileMentions = useMemo(() => fileMentionsResponse?.data ?? [], [fileMentionsResponse]);

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
  const effectiveAgentType: string = selectedModel ? agentTypeForModel(selectedModel) ?? defaultAgentType : defaultAgentType;
  const defaultReasoningEffort = getDefaultCodingAgentReasoningForAgent(user?.settings, effectiveAgentType);
  const effectiveReasoningOverride = isCodingAgentReasoningEffortSupported(effectiveAgentType, reasoningOverride) ? reasoningOverride : "";
  const effectiveReasoningEffort = effectiveReasoningOverride || defaultReasoningEffort;
  const showReasoningSelector = supportsReasoningEffort(effectiveAgentType);
  const submittedReasoningEffort = showReasoningSelector ? effectiveReasoningEffort : "";
  const reasoningOptions = getCodingAgentReasoningOptions(effectiveAgentType);
  const requiredProvider = AGENTS_BY_KEY[effectiveAgentType]?.providerKey ?? "";
  const hasAgentCredentials =
    resolvedCredentials.some((c) => c.provider === requiredProvider && c.source !== "none")
      || (effectiveAgentType === "codex" && codexAuthResponse?.data?.status === "completed");

  const slashCommandsQuery = useSessionComposerSlashCommands({
    agentType: effectiveAgentType,
    query: deferredCommandQuery,
    repositoryId: selectedRepoId || undefined,
    branch: selectedBranch || undefined,
    enabled: activeCommand !== null && !mentionDismissed,
  });
  const slashCommandGroups = useMemo(() => slashCommandsQuery.data?.groups ?? [], [slashCommandsQuery.data]);
  const slashCommandItems = useMemo(
    () => slashCommandGroups.flatMap((group) => group.items),
    [slashCommandGroups],
  );
  const showMentionPicker = !!selectedRepoId && activeMention !== null && !mentionDismissed;
  const showCommandPicker = activeCommand !== null && !mentionDismissed;

  const pickerGroups = useMemo<TriggerPickerGroup[]>(() => {
    if (showMentionPicker) {
      return [
        {
          id: "mentions",
          label: "Files and directories",
          items: fileMentions.map((reference) => ({
            id: `${reference.kind}:${reference.path ?? reference.id ?? reference.display}`,
            primary: reference.display,
            icon: reference.kind === "directory"
              ? <FolderTree className="h-4 w-4 shrink-0" />
              : <FileCode2 className="h-4 w-4 shrink-0" />,
          })),
        },
      ];
    }
    if (showCommandPicker) {
      return slashCommandGroups.map((group) => ({
        id: group.source,
        label: group.label,
        items: group.items.map((command) => ({
          id: command.name,
          primary: command.token,
          secondary: command.description,
          icon: <Slash className="h-4 w-4 shrink-0" />,
        })),
      }));
    }
    return [];
  }, [showMentionPicker, showCommandPicker, fileMentions, slashCommandGroups]);
  const flattenedPickerItems = useMemo(() => flattenGroups(pickerGroups), [pickerGroups]);

  const pickerLoading = showMentionPicker
    ? fileMentionsLoading
    : showCommandPicker
      ? slashCommandsQuery.isFetching
      : false;
  const pickerEmptyLabel = showCommandPicker
    ? `No commands for /${activeCommand?.query ?? ""}`
    : `No matches for @${activeMention?.query ?? ""}`;
  const pickerOpen = showMentionPicker || showCommandPicker;

  const invalidCommandTokens = useMemo(
    () => commands.filter((command) => command.agent_type !== effectiveAgentType).map((command) => command.token),
    [commands, effectiveAgentType],
  );
  const hasInvalidCommands = invalidCommandTokens.length > 0;

  const createManualSessionMutation = useMutation({
    mutationFn: () =>
      api.sessions.createManual({
        message: message.trim(),
        images: attachments,
        references,
        commands,
        ...(submittedReasoningEffort ? { reasoning_effort: submittedReasoningEffort } : {}),
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
      clearDraft();
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
    if (hasInvalidCommands) return;
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
  }, [triggerQueryKey]);

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
    setMessage((previous) => {
      const withoutReferences = references.reduce((next, reference) => removeMentionReference(next, reference), previous);
      return removeCommandsFromMessage(withoutReferences, projectCommandsOnly(commands));
    });
    setReferences([]);
    setCommands((previous) => previous.filter((command) => command.source !== "project"));
  }, [commands, references, selectedRepoId]);

  useEffect(() => {
    if (!pickerOpen) {
      setMentionPickerPosition(null);
      return;
    }

    function updateMentionPickerPosition() {
      const composerCard = composerCardRef.current;
      if (!composerCard) {
        return;
      }

      const rect = composerCard.getBoundingClientRect();
      const spacing = 12;
      const viewportHeight = window.innerHeight;
      const spaceAbove = rect.top - spacing;
      const spaceBelow = viewportHeight - rect.bottom - spacing;
      const side: "top" | "bottom" = spaceAbove >= 160 || spaceAbove >= spaceBelow ? "top" : "bottom";
      const availableHeight = Math.max(side === "top" ? spaceAbove : spaceBelow, 120);
      const top = side === "top"
        ? Math.max(spacing, rect.top - Math.min(320, availableHeight) - spacing)
        : Math.min(viewportHeight - spacing - Math.min(320, availableHeight), rect.bottom + spacing);

      setMentionPickerPosition({
        left: rect.left,
        top,
        width: rect.width,
        maxHeight: Math.min(320, availableHeight),
        side,
      });
    }

    updateMentionPickerPosition();
    window.addEventListener("resize", updateMentionPickerPosition);
    window.addEventListener("scroll", updateMentionPickerPosition, true);

    const composerCard = composerCardRef.current;
    const resizeObserver = composerCard && typeof ResizeObserver !== "undefined"
      ? new ResizeObserver(() => {
        updateMentionPickerPosition();
      })
      : null;
    if (composerCard && resizeObserver) {
      resizeObserver.observe(composerCard);
    }

    return () => {
      window.removeEventListener("resize", updateMentionPickerPosition);
      window.removeEventListener("scroll", updateMentionPickerPosition, true);
      resizeObserver?.disconnect();
    };
  }, [pickerOpen, fileMentions.length, fileMentionsLoading, slashCommandItems.length]);

  function updateMessage(nextMessage: string, nextCaret: number) {
    setMessage(nextMessage);
    setReferences((previous) => syncReferencesWithMessage(nextMessage, previous));
    setCommands((previous) => syncCommandsWithMessage(nextMessage, previous));
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

  function applyCommand(command: SessionInputCommand) {
    if (!activeCommand || !messageInputRef.current) {
      return;
    }
    const inserted = insertCommandAtCaret(message, activeCommand, command);
    setMessage(inserted.text);
    setCommands((previous) => {
      const existing = previous.find((item) => item.token === command.token);
      if (existing) {
        return syncCommandsWithMessage(inserted.text, previous);
      }
      return syncCommandsWithMessage(inserted.text, [...previous, command]);
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

  function removeCommand(command: SessionInputCommand) {
    const nextMessage = removeCommandReference(message, command);
    setMessage(nextMessage);
    setCommands((previous) => previous.filter((item) => item.token !== command.token));
    setCaretPosition(nextMessage.length);
  }

  async function uploadFiles(fileList: FileList | File[]) {
    const files = Array.from(fileList);
    if (files.length === 0) {
      return;
    }

    const oversized = files.filter((f) => f.size > MAX_FILE_SIZE);
    if (oversized.length > 0) {
      setCreationError(`File${oversized.length > 1 ? "s" : ""} too large (max 10 MB): ${oversized.map((f) => f.name).join(", ")}`);
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
    }
  }

  async function onUploadChange(event: React.ChangeEvent<HTMLInputElement>) {
    const fileList = event.target.files;
    if (!fileList || fileList.length === 0) {
      return;
    }

    await uploadFiles(fileList);
    event.target.value = "";
  }

  function isFileDrag(event: React.DragEvent<HTMLElement>) {
    const types = Array.from(event.dataTransfer.types ?? []);
    return types.includes("Files");
  }

  function summarizeDraggedFiles(files: File[]) {
    if (files.length === 0) {
      return "Drop images to attach";
    }
    const imageCount = files.filter((file) => file.type.startsWith("image/")).length;
    if (imageCount === files.length) {
      return files.length === 1 ? "Drop image to attach" : "Drop images to attach";
    }
    return "Drop files to attach";
  }

  function resetDragState() {
    dragDepthRef.current = 0;
    setIsDragActive(false);
    setDragMessage(null);
  }

  function handleDragEnter(event: React.DragEvent<HTMLDivElement>) {
    if (!isFileDrag(event)) {
      return;
    }
    event.preventDefault();
    dragDepthRef.current += 1;
    setIsDragActive(true);
    const files = Array.from(event.dataTransfer.files ?? []);
    setDragMessage(summarizeDraggedFiles(files));
  }

  function handleDragOver(event: React.DragEvent<HTMLDivElement>) {
    if (!isFileDrag(event)) {
      return;
    }
    event.preventDefault();
    event.dataTransfer.dropEffect = "copy";
    const files = Array.from(event.dataTransfer.files ?? []);
    setDragMessage(summarizeDraggedFiles(files));
    setIsDragActive(true);
  }

  function handleDragLeave(event: React.DragEvent<HTMLDivElement>) {
    if (!isFileDrag(event)) {
      return;
    }
    event.preventDefault();
    if (event.target !== event.currentTarget) {
      return;
    }
    const nextTarget = event.relatedTarget ?? (event.nativeEvent as DragEvent).relatedTarget;
    if (nextTarget instanceof Node && event.currentTarget.contains(nextTarget)) {
      return;
    }
    dragDepthRef.current = Math.max(0, dragDepthRef.current - 1);
    if (dragDepthRef.current === 0) {
      setIsDragActive(false);
      setDragMessage(null);
    }
  }

  async function handleDrop(event: React.DragEvent<HTMLDivElement>) {
    if (!isFileDrag(event)) {
      return;
    }
    event.preventDefault();
    const files = Array.from(event.dataTransfer.files ?? []);
    resetDragState();
    if (files.length === 0) {
      return;
    }
    await uploadFiles(files);
    requestAnimationFrame(() => {
      messageInputRef.current?.focus();
    });
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
      <div
        className="flex flex-1 flex-col px-4 pb-4"
        data-testid="manual-session-dropzone"
        data-drag-active={isDragActive ? "true" : "false"}
        onDragEnter={handleDragEnter}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
      >
        <div
          className={cn(
            "relative mx-auto flex w-full max-w-3xl flex-1 flex-col rounded-[2rem] border border-transparent transition-all duration-200 ease-out",
            "before:pointer-events-none before:absolute before:inset-0 before:rounded-[inherit] before:border before:border-dashed before:opacity-0 before:transition-opacity before:duration-200",
            "after:pointer-events-none after:absolute after:inset-0 after:rounded-[inherit] after:bg-[radial-gradient(circle_at_top,rgba(59,130,246,0.08),transparent_58%)] after:opacity-0 after:transition-opacity after:duration-200",
            isDragActive && "border-primary/20 bg-primary/5 shadow-[0_20px_70px_-40px_rgba(59,130,246,0.45)] before:opacity-100 before:border-primary/40 after:opacity-100",
          )}
        >
          <div className="pointer-events-none absolute inset-x-6 top-6 flex justify-center">
            <div
              aria-live="polite"
              className={cn(
                "inline-flex min-h-8 items-center rounded-full border px-3 py-1 text-xs font-medium tracking-[0.02em] text-muted-foreground transition-all duration-200",
                isDragActive
                  ? "translate-y-0 border-primary/30 bg-background/90 text-foreground opacity-100 shadow-sm"
                  : "translate-y-1 border-transparent bg-transparent opacity-0",
              )}
            >
              {dragMessage ?? "Drop images to attach"}
            </div>
          </div>

          {/* Centered hero + composer */}
          <div className="flex-1 flex flex-col items-center justify-center px-2 pt-16 pb-4">
            <div
              className={cn(
                "text-center mb-8 transition-all duration-200 ease-out",
                isDragActive ? "-translate-y-1 scale-[1.01]" : "translate-y-0 scale-100",
              )}
            >
              <p className={cn(
                "text-3xl font-semibold tracking-tight bg-[image:var(--gradient-primary)] bg-clip-text text-transparent transition-opacity duration-200",
                isDragActive ? "opacity-100" : "opacity-95",
              )}
              >
                Let&apos;s build
              </p>
              <p
                className={cn(
                  "mt-2 text-xs text-muted-foreground transition-all duration-200",
                  isDragActive ? "translate-y-0 opacity-100" : "translate-y-1 opacity-95",
                )}
              >
                {isDragActive
                  ? (dragMessage ?? "Drop images to attach")
                  : "Start a manual session with text, files, photos, dictation, or a screenshot anywhere here."}
              </p>
            </div>
          </div>

          {/* No repos warning */}
          {repositories.length === 0 && (
            <div className="shrink-0 px-0">
              <div className="w-full max-w-3xl mx-auto">
                <NoReposWarning />
              </div>
            </div>
          )}

          {/* Agent credentials warning */}
          {!hasAgentCredentials && (
            <div className="shrink-0 px-0">
              <div className="w-full max-w-3xl mx-auto">
                <AgentKeyRequiredBanner agentType={effectiveAgentType} />
              </div>
            </div>
          )}

          {/* Composer pinned to bottom */}
          <div className="shrink-0 px-0">
            <div className="relative mx-auto w-full max-w-3xl">
          <SessionComposerTriggerPicker
            open={pickerOpen}
            position={mentionPickerPosition}
            groups={pickerGroups}
            loading={pickerLoading}
            emptyLabel={pickerEmptyLabel}
            selectedIndex={selectedMentionIndex}
            onSelectedIndexChange={setSelectedMentionIndex}
            onSelect={(_item, group) => {
              const flatIndex = flattenedPickerItems.findIndex((entry) => entry.group.id === group.id && entry.item.id === _item.id);
              if (flatIndex < 0) return;
              if (showMentionPicker) {
                applyMention(fileMentions[flatIndex]);
                return;
              }
              if (showCommandPicker) {
                applyCommand(slashCommandItems[flatIndex]);
              }
            }}
            testId="mention-picker-overlay"
          />

          <Card
            ref={composerCardRef}
            className={cn(
              "w-full rounded-2xl border-border/60 bg-card shadow-lg transition-all duration-200 ease-out dark:shadow-[0_0_20px_oklch(0.6_0.15_270_/_6%)]",
              isDragActive && "-translate-y-0.5 border-primary/25 shadow-[0_24px_70px_-45px_rgba(59,130,246,0.55)]",
            )}
            data-testid="manual-session-composer"
          >
            <CardContent className="space-y-0 p-4">
              <Textarea
                ref={messageInputRef}
                value={message}
                autoFocus
                onChange={(event) => {
                  updateMessage(event.target.value, event.target.selectionStart ?? event.target.value.length);
                  resizeMessageInput();
                }}
                onClick={(event) => setCaretPosition(event.currentTarget.selectionStart ?? message.length)}
                onKeyUp={(event) => setCaretPosition(event.currentTarget.selectionStart ?? message.length)}
                onSelect={(event) => setCaretPosition(event.currentTarget.selectionStart ?? message.length)}
                onKeyDown={(event) => {
                  if (pickerOpen && flattenedPickerItems.length > 0) {
                    if (event.key === "ArrowDown") {
                      event.preventDefault();
                      setSelectedMentionIndex((previous) => (previous + 1) % flattenedPickerItems.length);
                      return;
                    }
                    if (event.key === "ArrowUp") {
                      event.preventDefault();
                      setSelectedMentionIndex((previous) => (previous - 1 + flattenedPickerItems.length) % flattenedPickerItems.length);
                      return;
                    }
                    if (event.key === "Enter" && !event.shiftKey) {
                      event.preventDefault();
                      const selection = flattenedPickerItems[selectedMentionIndex];
                      if (!selection) return;
                      if (showMentionPicker) {
                        applyMention(fileMentions[selectedMentionIndex]);
                      } else if (showCommandPicker) {
                        applyCommand(slashCommandItems[selectedMentionIndex]);
                      }
                      return;
                    }
                  }
                  if (pickerOpen && event.key === "Escape") {
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

            {(references.length > 0 || commands.length > 0) && (
              <div className="flex flex-wrap gap-2 pb-3" aria-label="Selected references and commands">
                {references.map((reference) => (
                  <Badge
                    key={`ref:${reference.kind}:${reference.path ?? reference.id ?? reference.display}`}
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
                {commands.map((command) => {
                  const isInvalid = command.agent_type !== effectiveAgentType;
                  return (
                    <Badge
                      key={`cmd:${command.token}`}
                      variant="secondary"
                      className={cn(
                        "gap-1 rounded-full border-border/60 bg-muted/60 pl-2 pr-1",
                        isInvalid && "border-amber-500/60 bg-amber-100/40 text-amber-900 dark:bg-amber-900/30 dark:text-amber-100",
                      )}
                      data-invalid={isInvalid || undefined}
                      title={isInvalid
                        ? `${command.token} is a ${command.agent_type} command. Switch agent or remove it.`
                        : undefined}
                    >
                      <Slash className="h-3 w-3" />
                      <span className="max-w-[18rem] truncate">{command.token}</span>
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        className="h-5 w-5 rounded-full"
                        aria-label={`Remove ${command.token}`}
                        onClick={() => removeCommand(command)}
                      >
                        <X className="h-3 w-3" />
                      </Button>
                    </Badge>
                  );
                })}
              </div>
            )}
            {hasInvalidCommands && (
              <p className="pb-3 text-xs text-amber-600 dark:text-amber-300" role="alert">
                {invalidCommandTokens.join(", ")} {invalidCommandTokens.length === 1 ? "is" : "are"} not valid for the selected agent. Remove the chip{invalidCommandTokens.length === 1 ? "" : "s"} or switch agents to continue.
              </p>
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
                <BranchPicker
                  repositoryId={selectedRepoId}
                  value={selectedBranch}
                  defaultBranch={selectedRepo.default_branch}
                  onValueChange={setSelectedBranch}
                  label="Target branch"
                  buttonClassName="h-8 rounded-full border-none bg-transparent px-3 text-xs text-muted-foreground shadow-none hover:bg-accent hover:text-foreground"
                  contentClassName="w-72"
                />
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

              {showReasoningSelector ? (
                <Select value={effectiveReasoningOverride || "__default__"} onValueChange={(v) => setReasoningOverride(v === "__default__" ? "" : toCodingAgentReasoningEffort(v))}>
                  <SelectTrigger className="h-8 w-auto gap-1.5 border-none bg-transparent px-2 text-xs text-muted-foreground shadow-none hover:text-foreground focus:ring-0" aria-label="Reasoning override">
                    <SelectValue placeholder="Reasoning" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="__default__">
                      {defaultReasoningEffort ? `Default (${defaultReasoningEffort})` : "Default"}
                    </SelectItem>
                    {reasoningOptions.map((option) => (
                      <SelectItem key={option.value} value={option.value}>
                        {option.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : null}

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
      </div>
    </div>
  );
}
