"use client";

import { memo, useCallback, useDeferredValue, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowUp,
  GitBranch,
  ChevronDown,
  FileCode2,
  FolderTree,
  Loader2,
  Slash,
  X,
  SlidersHorizontal,
} from "lucide-react";
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
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { BranchPicker } from "@/components/branch-picker";
import { LinearIcon } from "@/components/linear-icon";
import { looksLikeLinearRef } from "@/lib/linear-refs";
import { getClipboardFiles } from "@/lib/clipboard-files";
import { PendingAttachmentStrip } from "@/components/pending-attachment-strip";
import { SessionComposerAttachmentMenu } from "@/components/session-composer-attachment-menu";
import {
  SessionComposerTriggerPicker,
  flattenGroups,
  type TriggerPickerGroup,
  type TriggerPickerPosition,
} from "@/components/session-composer-trigger-picker";
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
import { useFileDropzone } from "@/hooks/use-file-dropzone";
import { clearDraft, loadDraft, saveDraft } from "@/lib/session-draft";
import { queryKeys } from "@/lib/query-keys";
import { applyCreatedSessionToSessionListCaches } from "@/lib/session-list-cache";
import { cn } from "@/lib/utils";
import {
  agentTypeForModel,
  availableAgentModelGroups,
  isAgentAvailable,
} from "@/lib/agents";
import { ModelOptionGroups } from "@/components/model-option-groups";
import {
  useOpenCodeAvailability,
  type OpenCodeModelAvailability,
} from "@/hooks/use-opencode-models";
import { SetupRequirementsCard } from "@/components/setup-requirements-card";
import { useOptimisticSessionsSafe } from "@/contexts/optimistic-sessions";
import { useAuth } from "@/hooks/use-auth";
import { useMediaQuery } from "@/hooks/use-media-query";
import {
  type CodingAgentReasoningEffort,
  getDefaultCodingAgentReasoningForAgent,
  getCodingAgentReasoningOptions,
  isCodingAgentReasoningEffortSupported,
  supportsReasoningEffort,
  toCodingAgentReasoningEffort,
} from "@/lib/coding-agent-reasoning";
import type {
  CodingCredentialSummary,
  Integration,
  OrgSettings,
  Organization,
  Repository,
  SingleResponse,
  ListResponse,
  SessionInputCommand,
  SessionInputReference,
} from "@/lib/types";

const MAX_FILE_SIZE = 10 * 1024 * 1024; // 10 MB
const DRAFT_SAVE_DEBOUNCE_MS = 400;
const DEFAULT_MANUAL_SESSION_TITLE = "Manual Session";
const triggerPickerIconClassName = "h-4 w-4 shrink-0";
const directoryTriggerIcon = <FolderTree className={triggerPickerIconClassName} />;
const fileTriggerIcon = <FileCode2 className={triggerPickerIconClassName} />;

type ComposerPickerPosition = TriggerPickerPosition;

function isDetachedReference(reference: SessionInputReference): boolean {
  return reference.kind === "app" || reference.kind === "plugin";
}

function syncComposerReferences(text: string, references: SessionInputReference[]): SessionInputReference[] {
  const inlineReferences = references.filter((reference) => !isDetachedReference(reference));
  const detachedReferences = references.filter((reference) => isDetachedReference(reference));
  return [...syncReferencesWithMessage(text, inlineReferences), ...detachedReferences];
}

function linearReferenceFromInput(input: string): SessionInputReference {
  const trimmed = input.trim();
  const keyMatch = trimmed.match(/([A-Z][A-Z0-9_]{0,9}-[0-9]+)/);
  const identifier = keyMatch?.[1] ?? trimmed;

  return {
    kind: "app",
    id: identifier,
    display: identifier,
    ...(trimmed !== identifier ? { token: trimmed } : {}),
  };
}

function referenceCarriesLinearRef(reference: SessionInputReference): boolean {
  return looksLikeLinearRef(reference.token ?? reference.id ?? reference.display);
}

export interface ManualSessionComposerProps {
  /** Called with the new session id once create succeeds. Caller decides routing. */
  onCreated: (sessionId: string) => void;
  /** When true, persists/restores form state via session-draft. Page only. */
  enableDrafts?: boolean;
  /** Autofocus the textarea when the component mounts. */
  autoFocus?: boolean;
  /** Initial repo id (e.g. from a URL `?repo=` param) — wins over draft repo. */
  initialRepoId?: string;
  /** Outer wrapper className (used by callers to set layout/sizing). */
  className?: string;
  /** Class on the inner div that constrains width and contains the card. */
  innerClassName?: string;
  /** Override the composer Card's className. */
  cardClassName?: string;
  /** Optional content rendered above the card (e.g. page hero text). */
  heroSlot?: ReactNode;
  /** Class applied to the textarea. */
  textareaClassName?: string;
  /** Placeholder for the textarea. Defaults to "Tell the agent what to do..." */
  placeholder?: string;
  /** Accessible label for the textarea. Defaults to "Session prompt". */
  textareaAriaLabel?: string;
  /** Show the live drop indicator pill inside the dropzone. Page only. */
  showDropIndicator?: boolean;
  /** Add a temporary sidebar row while create is pending. Disable when the route already renders a draft row. */
  showOptimisticSidebarRow?: boolean;
  /** Test id placed on the outer drop-target div. */
  dataTestId?: string;
}

type ComposerSettingsControlsProps = {
  repositories: Repository[];
  selectedRepoId: string;
  selectedRepo?: Repository;
  selectedBranch: string;
  modelGroups: ReturnType<typeof availableAgentModelGroups>;
  openCodeAvailability?: Map<string, OpenCodeModelAvailability>;
  selectedModel: string;
  showReasoningSelector: boolean;
  effectiveReasoningOverride: CodingAgentReasoningEffort;
  defaultReasoningEffort: CodingAgentReasoningEffort;
  reasoningOptions: ReturnType<typeof getCodingAgentReasoningOptions>;
  onRepoChange: (id: string) => void;
  onBranchChange: (branch: string) => void;
  onModelChange: (value: string) => void;
  onReasoningChange: (value: string) => void;
};

const ComposerSettingsControls = memo(function ComposerSettingsControls({
  repositories,
  selectedRepoId,
  selectedRepo,
  selectedBranch,
  modelGroups,
  openCodeAvailability,
  selectedModel,
  showReasoningSelector,
  effectiveReasoningOverride,
  defaultReasoningEffort,
  reasoningOptions,
  onRepoChange,
  onBranchChange,
  onModelChange,
  onReasoningChange,
}: ComposerSettingsControlsProps) {
  return (
    <>
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
                onClick={() => onRepoChange(repo.id)}
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
          onValueChange={onBranchChange}
          label="Target branch"
          buttonClassName="h-8 rounded-full border-none bg-transparent px-3 text-xs text-muted-foreground shadow-none hover:bg-accent hover:text-foreground"
          contentClassName="w-72"
        />
      )}

      <Select value={selectedModel} onValueChange={onModelChange}>
        <SelectTrigger className="h-8 w-auto gap-1.5 border-none bg-transparent px-2 text-xs text-muted-foreground shadow-none hover:text-foreground focus:ring-0" aria-label="Model override">
          <SelectValue placeholder="Default model" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="__default__">Default model</SelectItem>
          <ModelOptionGroups modelGroups={modelGroups} openCodeAvailability={openCodeAvailability} selectedModel={selectedModel} />
        </SelectContent>
      </Select>

      {showReasoningSelector ? (
        <Select value={effectiveReasoningOverride || "__default__"} onValueChange={onReasoningChange}>
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
    </>
  );
});

export function ManualSessionComposer({
  onCreated,
  enableDrafts = false,
  autoFocus = false,
  initialRepoId,
  className,
  innerClassName,
  cardClassName,
  heroSlot,
  textareaClassName,
  placeholder = "Tell the agent what to do...",
  textareaAriaLabel = "Session prompt",
  showDropIndicator = false,
  showOptimisticSidebarRow = true,
  dataTestId,
}: ManualSessionComposerProps) {
  const { user } = useAuth();
  const queryClient = useQueryClient();
  const uploadInputRef = useRef<HTMLInputElement>(null);
  const messageInputRef = useRef<HTMLTextAreaElement>(null);
  const composerCardRef = useRef<HTMLDivElement>(null);
  const draftSaveTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pendingDraftRef = useRef<Parameters<typeof saveDraft>[0] | null>(null);
  const resizeFrameRef = useRef<number | null>(null);
  // Synchronous guard: React Query's isPending flips on the next render, so
  // rapid Enter presses can all pass the isPending check in the same tick.
  const submittingRef = useRef(false);

  const [message, setMessage] = useState("");
  const [attachments, setAttachments] = useState<string[]>([]);
  const [references, setReferences] = useState<SessionInputReference[]>([]);
  const [commands, setCommands] = useState<SessionInputCommand[]>([]);
  const [isUploading, setIsUploading] = useState(false);
  const [showImageInput, setShowImageInput] = useState(false);
  const [imageURL, setImageURL] = useState("");
  const [showLinearInput, setShowLinearInput] = useState(false);
  const [linearInput, setLinearInput] = useState("");
  const [linearInputError, setLinearInputError] = useState<string | null>(null);
  const linearInputRef = useRef<HTMLInputElement>(null);

  // Focus the Linear input the render after it mounts. Layout effect (vs.
  // requestAnimationFrame inside the menu item's onClick) guarantees the
  // input is committed to the DOM before we focus, so the first menu open
  // doesn't race React's render and silently drop focus.
  useEffect(() => {
    if (showLinearInput) {
      linearInputRef.current?.focus();
    }
  }, [showLinearInput]);
  const [selectedModel, setSelectedModel] = useState("");
  const [reasoningOverride, setReasoningOverride] = useState<CodingAgentReasoningEffort>("");
  const [userSelectedRepoId, setUserSelectedRepoId] = useState<string | null>(initialRepoId ?? null);
  const [branchByRepoId, setBranchByRepoId] = useState<Record<string, string>>({});
  const [creationError, setCreationError] = useState<string | null>(null);
  const [isNavigatingAfterCreate, setIsNavigatingAfterCreate] = useState(false);
  const [caretPosition, setCaretPosition] = useState(0);
  const [selectedTriggerIndex, setSelectedTriggerIndex] = useState(0);
  const [triggerDismissed, setTriggerDismissed] = useState(false);
  const [pickerPosition, setPickerPosition] = useState<ComposerPickerPosition | null>(null);
  const [mobileSettingsOpen, setMobileSettingsOpen] = useState(false);
  // Gates the persist effect until after hydration so we never overwrite a
  // stored draft with the component's initial (empty) state on first mount.
  // When drafts are disabled, we treat the component as already "hydrated"
  // so dependent effects don't stall.
  const [draftHydrated, setDraftHydrated] = useState(!enableDrafts);
  const previousRepoIdRef = useRef<string>("");

  const { addOptimisticSession, removeOptimisticSession, markOptimisticResolved } = useOptimisticSessionsSafe();
  const isMobile = useMediaQuery("(max-width: 767px)");

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
    if (!enableDrafts) return;
    const draft = loadDraft();
    if (draft) {
      const repoConflict =
        !!initialRepoId && !!draft.userSelectedRepoId && initialRepoId !== draft.userSelectedRepoId;

      setAttachments(draft.attachments);
      setSelectedModel(draft.selectedModel);
      setReasoningOverride(draft.reasoningOverride);
      setShowImageInput(draft.showImageInput);
      setImageURL(draft.imageURL);
      setBranchByRepoId(draft.branchByRepoId);
      if (!initialRepoId) {
        setUserSelectedRepoId(draft.userSelectedRepoId);
      }

      const draftProjectCommands = projectCommandsOnly(draft.commands);
      if (repoConflict) {
        const inlineDraftReferences = draft.references.filter((reference) => !isDetachedReference(reference));
        const detachedDraftReferences = draft.references.filter((reference) => isDetachedReference(reference));
        const strippedReferences = inlineDraftReferences.reduce(
          (text, reference) => removeMentionReference(text, reference),
          draft.message,
        );
        setMessage(removeCommandsFromMessage(strippedReferences, draftProjectCommands));
        setReferences(detachedDraftReferences);
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
  // state (caret, mention picker, errors, upload-in-flight flag)
  // is deliberately excluded — restoring it on reload would be meaningless or
  // confusing.
  useEffect(() => {
    if (!enableDrafts) return;
    if (!draftHydrated) return;
    const nextDraft = {
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
    };

    pendingDraftRef.current = nextDraft;
    if (draftSaveTimerRef.current) {
      clearTimeout(draftSaveTimerRef.current);
    }
    draftSaveTimerRef.current = setTimeout(() => {
      if (!pendingDraftRef.current) return;
      saveDraft(pendingDraftRef.current);
      pendingDraftRef.current = null;
      draftSaveTimerRef.current = null;
    }, DRAFT_SAVE_DEBOUNCE_MS);
  }, [
    enableDrafts,
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

  function flushDraftSave() {
    if (!enableDrafts) return;
    if (draftSaveTimerRef.current) {
      clearTimeout(draftSaveTimerRef.current);
      draftSaveTimerRef.current = null;
    }
    if (!pendingDraftRef.current) {
      return;
    }
    saveDraft(pendingDraftRef.current);
    pendingDraftRef.current = null;
  }

  function discardDraftSave() {
    if (!enableDrafts) return;
    if (draftSaveTimerRef.current) {
      clearTimeout(draftSaveTimerRef.current);
      draftSaveTimerRef.current = null;
    }
    pendingDraftRef.current = null;
    clearDraft();
  }

  useEffect(() => {
    return () => {
      flushDraftSave();
      if (resizeFrameRef.current !== null) {
        cancelAnimationFrame(resizeFrameRef.current);
      }
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const {
    data: settingsResponse,
    isPending: settingsPending,
    isSuccess: settingsLoaded,
  } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });

  const settings = settingsResponse?.data?.settings as OrgSettings | undefined;
  const defaultAgentType = settings?.default_agent_type ?? "codex";
  const defaultWorkRepositoryId = settings?.default_work_repository_id ?? "";

  const { data: reposResponse, isSuccess: reposLoaded } = useQuery<ListResponse<Repository>>({
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

  // The unified resolved stack (personal rows first, then the org fallback)
  // carries agent + status per row, so it powers both the model picker and
  // the missing-credentials banner without a separate org-stack query.
  const { data: resolvedCredsResponse, isSuccess: resolvedCredsLoaded } = useQuery<ListResponse<CodingCredentialSummary>>({
    queryKey: queryKeys.codingCredentials.list("resolved"),
    queryFn: () => api.codingCredentials.list("resolved"),
  });
  const resolvedCredentials = useMemo(() => resolvedCredsResponse?.data ?? [], [resolvedCredsResponse]);

  const { data: codexAuthResponse, isSuccess: codexAuthLoaded } = useQuery({
    queryKey: queryKeys.codexAuth.status,
    queryFn: () => api.codexAuth.status(),
  });
  const { data: integrationsResponse, isSuccess: integrationsLoaded } = useQuery<ListResponse<Integration>>({
    queryKey: queryKeys.integrations.all,
    queryFn: () => api.integrations.list(),
  });

  const isAutoRepoSelectionPending = userSelectedRepoId === null && repositories.length > 1 && settingsPending;

  // Auto-select: user's explicit choice > org default work repo > last used repo > first repo.
  const selectedRepoId = useMemo(() => {
    if (userSelectedRepoId !== null) return userSelectedRepoId;
    if (repositories.length === 1) return repositories[0].id;
    if (repositories.length > 0) {
      if (isAutoRepoSelectionPending) return "";
      if (defaultWorkRepositoryId && repositories.some((r) => r.id === defaultWorkRepositoryId)) {
        return defaultWorkRepositoryId;
      }
      try {
        const lastUsed = localStorage.getItem("143:lastUsedRepoId");
        if (lastUsed && repositories.some((r) => r.id === lastUsed)) return lastUsed;
      } catch {}
      return repositories[0].id;
    }
    return "";
  }, [defaultWorkRepositoryId, isAutoRepoSelectionPending, userSelectedRepoId, repositories]);

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
    enabled: !!selectedRepoId && activeMention !== null && !triggerDismissed,
    staleTime: 30 * 1000,
  });
  const fileMentions = useMemo(() => fileMentionsResponse?.data ?? [], [fileMentionsResponse]);

  const codexAuthStatus = codexAuthResponse?.data;
  const userDefaultModel = user?.settings?.coding_agent_model_default ?? "";
  // Sessions run under the user's resolved credential stack (personal rows
  // ahead of the org fallback), so we don't pass orgAgentConfig — agents the
  // org has raw env-var keys for but the user can't resolve are intentionally
  // hidden from the picker.
  const modelGroups = useMemo(
    () =>
      availableAgentModelGroups(
        [],
        codexAuthStatus,
        resolvedCredentials,
        defaultAgentType,
      ),
    [defaultAgentType, resolvedCredentials, codexAuthStatus],
  );
  const openCodeAvailability = useOpenCodeAvailability(resolvedCredentials);

  // Drop a previously selected model (from React state or restored draft) when
  // its agent is no longer integrated — keeps the picker value consistent with
  // what's renderable. Only act once all availability queries have resolved so
  // a transient loading state doesn't nuke a valid choice.
  useEffect(() => {
    if (!resolvedCredsResponse || !codexAuthResponse) return;
    if (!selectedModel) return;
    const stillAvailable = modelGroups.some((g) => g.models.includes(selectedModel));
    if (!stillAvailable) setSelectedModel("");
  }, [modelGroups, selectedModel, resolvedCredsResponse, codexAuthResponse]);

  // Determine which agent type would be used and whether credentials exist.
  const submittedModel = selectedModel || userDefaultModel;
  const effectiveAgentType: string = submittedModel ? agentTypeForModel(submittedModel) ?? defaultAgentType : defaultAgentType;
  const defaultReasoningEffort = getDefaultCodingAgentReasoningForAgent(user?.settings, effectiveAgentType);
  const effectiveReasoningOverride = isCodingAgentReasoningEffortSupported(effectiveAgentType, reasoningOverride) ? reasoningOverride : "";
  const effectiveReasoningEffort = effectiveReasoningOverride || defaultReasoningEffort;
  const showReasoningSelector = supportsReasoningEffort(effectiveAgentType);
  const submittedReasoningEffort = showReasoningSelector ? effectiveReasoningEffort : "";
  const reasoningOptions = useMemo(
    () => getCodingAgentReasoningOptions(effectiveAgentType),
    [effectiveAgentType],
  );
  const hasAgentCredentials = isAgentAvailable(effectiveAgentType, [], codexAuthStatus, resolvedCredentials);
  const agentCredentialStateLoaded = settingsLoaded && resolvedCredsLoaded && codexAuthLoaded;
  const shouldShowAgentKeyRequiredBanner = agentCredentialStateLoaded && !hasAgentCredentials;
  const linearConnected = integrationsLoaded && (integrationsResponse?.data ?? []).some(
    (integration) => integration.provider === "linear" && integration.status === "active",
  );

  const handleRepoChange = useCallback((id: string) => {
    setUserSelectedRepoId(id);
  }, []);

  const handleBranchChange = useCallback((branch: string) => {
    if (!selectedRepoId) return;
    setBranchByRepoId((prev) => ({ ...prev, [selectedRepoId]: branch }));
  }, [selectedRepoId]);

  const handleModelChange = useCallback((value: string) => {
    setSelectedModel(value === "__default__" ? "" : value);
  }, []);

  const handleReasoningChange = useCallback((value: string) => {
    setReasoningOverride(value === "__default__" ? "" : toCodingAgentReasoningEffort(value));
  }, []);

  const slashCommandsQuery = useSessionComposerSlashCommands({
    agentType: effectiveAgentType,
    query: deferredCommandQuery,
    repositoryId: selectedRepoId || undefined,
    branch: selectedBranch || undefined,
    enabled: activeCommand !== null && !triggerDismissed,
  });
  const slashCommandGroups = useMemo(() => slashCommandsQuery.data?.groups ?? [], [slashCommandsQuery.data]);
  const slashCommandItems = useMemo(
    () => slashCommandGroups.flatMap((group) => group.items),
    [slashCommandGroups],
  );
  const showMentionPicker = !!selectedRepoId && activeMention !== null && !triggerDismissed;
  const showCommandPicker = activeCommand !== null && !triggerDismissed;

  const pickerGroups = useMemo<TriggerPickerGroup[]>(() => {
    if (showMentionPicker) {
      return [
        {
          id: "mentions",
          label: "Files and directories",
          items: fileMentions.map((reference) => ({
            id: `${reference.kind}:${reference.path ?? reference.id ?? reference.display}`,
            primary: reference.display,
            icon: reference.kind === "directory" ? directoryTriggerIcon : fileTriggerIcon,
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
  const hasSubmittableInput = useMemo(
    () =>
      message.trim().length > 0 ||
      attachments.length > 0 ||
      references.some((reference) => referenceCarriesLinearRef(reference)),
    [attachments, message, references],
  );

  const createManualSessionMutation = useMutation({
    mutationFn: () =>
      api.sessions.createManual({
        message: message.trim(),
        images: attachments,
        references,
        commands,
        ...(submittedReasoningEffort ? { reasoning_effort: submittedReasoningEffort } : {}),
        ...(submittedModel ? { model: submittedModel, agent_type: agentTypeForModel(submittedModel) } : {}),
        ...(selectedRepoId ? { repository_id: selectedRepoId } : {}),
        ...(selectedBranch ? { branch: selectedBranch } : {}),
      }),
    onMutate: () => {
      setCreationError(null);
      setIsNavigatingAfterCreate(false);
      const rawTitle = message.trim().length > 0
        ? message.trim()
        : references.find((reference) => referenceCarriesLinearRef(reference))?.display ?? "";
      const optimisticTitle = rawTitle || DEFAULT_MANUAL_SESSION_TITLE;
      const title = optimisticTitle.length > 80
        ? optimisticTitle.slice(0, 80) + "..."
        : optimisticTitle;
      return { optimisticId: showOptimisticSidebarRow ? addOptimisticSession(title) : undefined };
    },
    onSuccess: (response, _variables, context) => {
      if (selectedRepoId) {
        try { localStorage.setItem("143:lastUsedRepoId", selectedRepoId); } catch {}
      }
      discardDraftSave();
      // Keep the optimistic row visible — the sidebar swaps it for the real
      // session once the refetch lands. See OptimisticSession.resolvedId.
      if (context.optimisticId) {
        markOptimisticResolved(context.optimisticId, response.data.id);
      }
      applyCreatedSessionToSessionListCaches(queryClient, response.data);
      queryClient.invalidateQueries({ queryKey: queryKeys.sessions.all });
      setIsNavigatingAfterCreate(true);
      onCreated(response.data.id);
    },
    onError: (error, _variables, context) => {
      captureError(error, { feature: "session-create" });
      if (context?.optimisticId) {
        removeOptimisticSession(context.optimisticId);
      }
      setCreationError(
        error instanceof Error ? error.message : "Could not start session. Please try again.",
      );
      setIsNavigatingAfterCreate(false);
      submittingRef.current = false;
    },
  });
  const isCreatingSession = createManualSessionMutation.isPending || isNavigatingAfterCreate;

  function submitManualSession() {
    if (submittingRef.current) return;
    if (!hasSubmittableInput) return;
    if (hasInvalidCommands) return;
    if (isAutoRepoSelectionPending) return;
    submittingRef.current = true;
    createManualSessionMutation.mutate();
  }

  function resizeMessageInput() {
    if (resizeFrameRef.current !== null) {
      cancelAnimationFrame(resizeFrameRef.current);
    }

    resizeFrameRef.current = requestAnimationFrame(() => {
      resizeFrameRef.current = null;
      const element = messageInputRef.current;
      if (!element) {
        return;
      }

      const maxHeight = 240;
      element.style.height = "auto";
      const nextHeight = Math.min(element.scrollHeight, maxHeight);
      element.style.height = `${nextHeight}px`;
      element.style.overflowY = element.scrollHeight > maxHeight ? "auto" : "hidden";
    });
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
    setTriggerDismissed(false);
    setSelectedTriggerIndex(0);
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
      const withoutReferences = references
        .filter((reference) => !isDetachedReference(reference))
        .reduce((next, reference) => removeMentionReference(next, reference), previous);
      return removeCommandsFromMessage(withoutReferences, projectCommandsOnly(commands));
    });
    setReferences((previous) => previous.filter((reference) => isDetachedReference(reference)));
    setCommands((previous) => previous.filter((command) => command.source !== "project"));
  }, [commands, references, selectedRepoId]);

  useEffect(() => {
    if (!pickerOpen) {
      setPickerPosition(null);
      return;
    }

    function updatePickerPosition() {
      const composerCard = composerCardRef.current;
      if (!composerCard) {
        return;
      }

      const rect = composerCard.getBoundingClientRect();
      const spacing = 12;
      const viewportHeight = window.innerHeight;
      const availableHeight = Math.max(rect.top - spacing, 120);
      setPickerPosition({
        left: rect.left,
        bottom: viewportHeight - rect.top + spacing,
        width: rect.width,
        maxHeight: Math.min(320, availableHeight),
      });
    }

    updatePickerPosition();
    window.addEventListener("resize", updatePickerPosition);
    window.addEventListener("scroll", updatePickerPosition, true);

    const composerCard = composerCardRef.current;
    const resizeObserver = composerCard && typeof ResizeObserver !== "undefined"
      ? new ResizeObserver(() => {
        updatePickerPosition();
      })
      : null;
    if (composerCard && resizeObserver) {
      resizeObserver.observe(composerCard);
    }

    return () => {
      window.removeEventListener("resize", updatePickerPosition);
      window.removeEventListener("scroll", updatePickerPosition, true);
      resizeObserver?.disconnect();
    };
  }, [pickerOpen, fileMentions.length, fileMentionsLoading, slashCommandItems.length]);

  function updateMessage(nextMessage: string, nextCaret: number) {
    setMessage(nextMessage);
    setReferences((previous) => syncComposerReferences(nextMessage, previous));
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
        return syncComposerReferences(inserted.text, previous);
      }
      return syncComposerReferences(inserted.text, [...previous, reference]);
    });
    setCaretPosition(inserted.caret);
    setTriggerDismissed(false);

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
    setTriggerDismissed(false);

    requestAnimationFrame(() => {
      if (!messageInputRef.current) {
        return;
      }
      messageInputRef.current.focus();
      messageInputRef.current.setSelectionRange(inserted.caret, inserted.caret);
    });
  }

  function removeReference(reference: SessionInputReference) {
    const nextMessage = isDetachedReference(reference) ? message : removeMentionReference(message, reference);
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

  const fileDropzone = useFileDropzone({
    onFilesDropped: uploadFiles,
    getDragMessage: summarizeDraggedFiles,
    onAfterDrop: () => {
      requestAnimationFrame(() => {
        messageInputRef.current?.focus();
      });
    },
  });

  async function handlePaste(event: React.ClipboardEvent<HTMLTextAreaElement>) {
    const files = getClipboardFiles(event.clipboardData);
    if (files.length === 0) {
      return;
    }

    event.preventDefault();
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

  function addLinearLink() {
    const trimmed = linearInput.trim();
    if (!trimmed) {
      return;
    }
    if (!looksLikeLinearRef(trimmed)) {
      // UX-only validation. The backend ResolveAndLinkAtCreate re-validates
      // with the org's team-key allowlist, so this catches obvious garbage
      // (free text, typos) without claiming to be authoritative.
      setLinearInputError("Enter a Linear URL (https://linear.app/...) or key like ACS-1234");
      return;
    }
    const reference = linearReferenceFromInput(trimmed);
    setReferences((previous) => {
      const alreadyLinked = previous.some((item) => item.kind === reference.kind && item.id === reference.id);
      if (alreadyLinked) {
        return previous;
      }
      return [...previous, reference];
    });
    setLinearInput("");
    setLinearInputError(null);
    setShowLinearInput(false);
    requestAnimationFrame(() => {
      messageInputRef.current?.focus();
    });
  }

  function removeAttachment(value: string) {
    setAttachments((previous) => previous.filter((item) => item !== value));
  }

  const repoSummary = selectedRepo ? selectedRepo.full_name.split("/").pop() ?? selectedRepo.full_name : "No repo";
  const modelSummary = selectedModel || (userDefaultModel ? `Default (${userDefaultModel})` : "Default model");
  const reasoningSummary = effectiveReasoningEffort || "Default reasoning";

  const settingsControls = (
    <div className="space-y-4">
      {repositories.length > 0 && (
        <div className="space-y-2">
          <Label className="text-xs uppercase tracking-[0.14em] text-muted-foreground">Repository</Label>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline" className="h-11 w-full justify-between rounded-xl border-border/70 bg-background px-3 text-left">
                <span className="flex items-center gap-2 overflow-hidden">
                  <GitBranch className="h-4 w-4 shrink-0 text-muted-foreground" />
                  <span className="truncate">{selectedRepo ? selectedRepo.full_name : "Select repository"}</span>
                </span>
                <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" className="w-72">
              {repositories.map((repo) => (
                <DropdownMenuItem
                  key={repo.id}
                  onClick={() => handleRepoChange(repo.id)}
                  className={selectedRepoId === repo.id ? "font-medium" : ""}
                >
                  <GitBranch className="mr-2 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                  <span className="truncate">{repo.full_name}</span>
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      )}

      {selectedRepo && (
        <div className="space-y-2">
          <Label className="text-xs uppercase tracking-[0.14em] text-muted-foreground">Branch</Label>
          <BranchPicker
            repositoryId={selectedRepoId}
            value={selectedBranch}
            defaultBranch={selectedRepo.default_branch}
            onValueChange={handleBranchChange}
            label="Target branch"
            buttonClassName="h-11 w-full justify-between rounded-xl border border-border/70 bg-background px-3 text-left text-sm shadow-none hover:bg-accent/60"
            contentClassName="w-72"
          />
        </div>
      )}

      <div className="space-y-2">
        <Label className="text-xs uppercase tracking-[0.14em] text-muted-foreground">Model</Label>
        <Select value={selectedModel} onValueChange={handleModelChange}>
          <SelectTrigger className="h-11 rounded-xl border-border/70 bg-background text-sm" aria-label="Model override">
            <SelectValue placeholder="Default model" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="__default__">Default model</SelectItem>
            <ModelOptionGroups modelGroups={modelGroups} openCodeAvailability={openCodeAvailability} selectedModel={selectedModel} />
          </SelectContent>
        </Select>
      </div>

      {showReasoningSelector ? (
        <div className="space-y-2">
          <Label className="text-xs uppercase tracking-[0.14em] text-muted-foreground">Reasoning</Label>
          <Select value={effectiveReasoningOverride || "__default__"} onValueChange={handleReasoningChange}>
            <SelectTrigger className="h-11 rounded-xl border-border/70 bg-background text-sm" aria-label="Reasoning override">
              <SelectValue placeholder="Default reasoning" />
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
        </div>
      ) : null}
    </div>
  );

  return (
    <div
      className={cn(
        "relative flex flex-col rounded-[2rem] border border-transparent transition-all duration-200 ease-out",
        "before:pointer-events-none before:absolute before:inset-0 before:rounded-[inherit] before:border before:border-dashed before:opacity-0 before:transition-opacity before:duration-200",
        fileDropzone.isDragActive && "border-primary/25 bg-primary/5 ring-2 ring-primary/10 before:border-primary/45 before:opacity-100",
        className,
      )}
      data-testid={dataTestId}
      {...fileDropzone.dropzoneProps}
    >
      {showDropIndicator && (
        <div className="pointer-events-none absolute inset-x-6 top-6 flex justify-center">
          <div
            aria-live="polite"
            className={cn(
              "inline-flex min-h-8 items-center rounded-full border px-3 py-1 text-xs font-medium tracking-[0.02em] text-muted-foreground transition-all duration-200",
              fileDropzone.isDragActive
                ? "translate-y-0 border-primary/30 bg-background/90 text-foreground opacity-100 shadow-sm"
                : "translate-y-1 border-transparent bg-transparent opacity-0",
            )}
          >
            {fileDropzone.dragMessage ?? "Drop images to attach"}
          </div>
        </div>
      )}

      {heroSlot}

      {(shouldShowAgentKeyRequiredBanner || (reposLoaded && repositories.length === 0)) && (
        <div className="shrink-0 px-0">
          <div className={cn("w-full mx-auto", innerClassName)}>
            <SetupRequirementsCard
              showAgentRow={shouldShowAgentKeyRequiredBanner}
              agentType={effectiveAgentType}
              showRepoRow={reposLoaded && repositories.length === 0}
            />
          </div>
        </div>
      )}

      <div className="shrink-0 px-0">
        <div className={cn("relative mx-auto w-full", innerClassName)}>
          <SessionComposerTriggerPicker
            open={pickerOpen}
            position={pickerPosition}
            groups={pickerGroups}
            loading={pickerLoading}
            emptyLabel={pickerEmptyLabel}
            selectedIndex={selectedTriggerIndex}
            onSelectedIndexChange={setSelectedTriggerIndex}
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
            variant="elevated"
            className={cn(
              "w-full rounded-2xl border-border-strong bg-card transition-all duration-200 ease-out",
              fileDropzone.isDragActive && "-translate-y-0.5 border-primary/35 ring-2 ring-primary/10",
              cardClassName,
            )}
            data-testid="manual-session-composer"
          >
            <CardContent className="space-y-0 p-4">
              <Textarea
                ref={messageInputRef}
                value={message}
                autoFocus={autoFocus}
                onChange={(event) => {
                  updateMessage(event.target.value, event.target.selectionStart ?? event.target.value.length);
                }}
                onPaste={handlePaste}
                onBlur={flushDraftSave}
                onClick={(event) => setCaretPosition(event.currentTarget.selectionStart ?? message.length)}
                onKeyUp={(event) => setCaretPosition(event.currentTarget.selectionStart ?? message.length)}
                onSelect={(event) => setCaretPosition(event.currentTarget.selectionStart ?? message.length)}
                onKeyDown={(event) => {
                  if (pickerOpen && flattenedPickerItems.length > 0) {
                    if (event.key === "ArrowDown") {
                      event.preventDefault();
                      setSelectedTriggerIndex((previous) => (previous + 1) % flattenedPickerItems.length);
                      return;
                    }
                    if (event.key === "ArrowUp") {
                      event.preventDefault();
                      setSelectedTriggerIndex((previous) => (previous - 1 + flattenedPickerItems.length) % flattenedPickerItems.length);
                      return;
                    }
                    if (event.key === "Enter" && !event.shiftKey) {
                      event.preventDefault();
                      const selection = flattenedPickerItems[selectedTriggerIndex];
                      if (!selection) return;
                      if (showMentionPicker) {
                        applyMention(fileMentions[selectedTriggerIndex]);
                      } else if (showCommandPicker) {
                        applyCommand(slashCommandItems[selectedTriggerIndex]);
                      }
                      return;
                    }
                  }
                  if (pickerOpen && event.key === "Escape") {
                    event.preventDefault();
                    setTriggerDismissed(true);
                    return;
                  }
                  if (event.key === "Enter" && !event.shiftKey) {
                    event.preventDefault();
                    submitManualSession();
                  }
                }}
                placeholder={placeholder}
                rows={1}
                disabled={isCreatingSession}
                className={cn(
                  "min-h-[44px] resize-none border-none bg-transparent px-0 py-2 shadow-none placeholder:text-muted-foreground/60 focus-visible:ring-0 disabled:opacity-60 disabled:cursor-not-allowed",
                  textareaClassName,
                )}
                aria-label={textareaAriaLabel}
              />

              {(references.length > 0 || commands.length > 0) && (
                <div className="flex flex-wrap gap-2 pb-3" aria-label="Selected references and commands">
                  {references.map((reference) => (
                    <Badge
                      key={`ref:${reference.kind}:${reference.path ?? reference.id ?? reference.display}`}
                      variant="secondary"
                      className="gap-1 rounded-full border-border/60 bg-muted/60 pl-2 pr-1"
                    >
                      {reference.kind === "directory"
                        ? <FolderTree className="h-3 w-3" />
                        : reference.kind === "app" && referenceCarriesLinearRef(reference)
                          ? <LinearIcon className="h-3 w-3" />
                          : <FileCode2 className="h-3 w-3" />}
                      <span className="max-w-[18rem] truncate">{reference.display}</span>
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon-compact"
                        className="rounded-full"
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
                          isInvalid && "border-warning/60 bg-warning/10 text-warning",
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
                          size="icon-compact"
                          className="rounded-full"
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
                <p className="pb-3 text-xs text-warning" role="alert">
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

              {showLinearInput && (
                <div className="flex flex-col gap-1 pb-3">
                  <div className="flex items-center gap-2">
                    <LinearIcon className="h-4 w-4 shrink-0 text-muted-foreground" />
                    <Input
                      ref={linearInputRef}
                      value={linearInput}
                      onChange={(event) => {
                        setLinearInput(event.target.value);
                        if (linearInputError) {
                          setLinearInputError(null);
                        }
                      }}
                      onKeyDown={(event) => {
                        if (event.key === "Enter") {
                          event.preventDefault();
                          addLinearLink();
                        } else if (event.key === "Escape") {
                          event.preventDefault();
                          setLinearInput("");
                          setLinearInputError(null);
                          setShowLinearInput(false);
                        }
                      }}
                      placeholder="ACS-1234 or https://linear.app/acme/issue/ACS-1234"
                      aria-label="Linear issue id or URL"
                      aria-invalid={linearInputError ? true : undefined}
                    />
                    <Button type="button" variant="outline" onClick={addLinearLink}>
                      Add
                    </Button>
                  </div>
                  {linearInputError && (
                    <p role="alert" className="pl-6 text-xs text-destructive">{linearInputError}</p>
                  )}
                </div>
              )}

              <div className="pt-2">
                {isMobile ? (
                  <>
                    <div className="flex items-center gap-2">
                      <SessionComposerAttachmentMenu
                        buttonAriaLabel="Add files or photos"
                        buttonClassName="h-8 w-8 rounded-full text-muted-foreground hover:text-foreground"
                        onUploadFiles={() => uploadInputRef.current?.click()}
                        onAddImageURL={() => setShowImageInput(true)}
                        onAddLinearIssue={() => setShowLinearInput(true)}
                        showLinearIssue={linearConnected}
                      />
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        aria-label="Session settings"
                        className="h-8 rounded-full border border-border/60 bg-muted/40 px-3 text-xs text-foreground shadow-sm hover:bg-accent/70"
                        onClick={() => setMobileSettingsOpen(true)}
                      >
                        <SlidersHorizontal className="mr-1.5 h-3.5 w-3.5" />
                        Settings
                      </Button>
                      <Button
                        type="button"
                        size="icon"
                        onClick={submitManualSession}
                        disabled={!hasSubmittableInput || isCreatingSession || isAutoRepoSelectionPending}
                        className="ml-auto h-8 w-8 rounded-full"
                        aria-label="Start session"
                      >
                        {isCreatingSession ? (
                          <Loader2 className="h-4 w-4 animate-spin" />
                        ) : (
                          <ArrowUp className="h-4 w-4" />
                        )}
                      </Button>
                    </div>
                    <div className="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
                      <span className="truncate font-medium text-foreground">{repoSummary}</span>
                      <span aria-hidden="true">•</span>
                      <span>{selectedBranch || "No branch"}</span>
                      <span aria-hidden="true">•</span>
                      <span>{modelSummary}</span>
                      <span aria-hidden="true">•</span>
                      <span>{reasoningSummary}</span>
                    </div>
                  </>
                ) : (
                  <div className="flex items-center gap-1">
                    <SessionComposerAttachmentMenu
                      buttonAriaLabel="Add files or photos"
                      buttonClassName="h-8 w-8 rounded-full text-muted-foreground hover:text-foreground"
                      onUploadFiles={() => uploadInputRef.current?.click()}
                      onAddImageURL={() => setShowImageInput(true)}
                      onAddLinearIssue={() => setShowLinearInput(true)}
                      showLinearIssue={linearConnected}
                    />

                    <ComposerSettingsControls
                      repositories={repositories}
                      selectedRepoId={selectedRepoId}
                      selectedRepo={selectedRepo}
                      selectedBranch={selectedBranch}
                      modelGroups={modelGroups}
                      openCodeAvailability={openCodeAvailability}
                      selectedModel={selectedModel}
                      showReasoningSelector={showReasoningSelector}
                      effectiveReasoningOverride={effectiveReasoningOverride}
                      defaultReasoningEffort={defaultReasoningEffort}
                      reasoningOptions={reasoningOptions}
                      onRepoChange={handleRepoChange}
                      onBranchChange={handleBranchChange}
                      onModelChange={handleModelChange}
                      onReasoningChange={handleReasoningChange}
                    />

                    <Button
                      type="button"
                      size="icon"
                      onClick={submitManualSession}
                      disabled={!hasSubmittableInput || isCreatingSession || isAutoRepoSelectionPending}
                      className="ml-auto h-8 w-8 rounded-full"
                      aria-label="Start session"
                    >
                      {isCreatingSession ? (
                        <Loader2 className="h-4 w-4 animate-spin" />
                      ) : (
                        <ArrowUp className="h-4 w-4" />
                      )}
                    </Button>
                  </div>
                )}
                <Input
                  ref={uploadInputRef}
                  type="file"
                  accept="image/png,image/jpeg,image/gif,image/webp,.heic,.heif,.pdf,.txt,.md,.json,.csv"
                  multiple
                  className="hidden"
                  onChange={onUploadChange}
                />
              </div>

              {creationError && (
                <p className="pt-2 text-xs text-destructive">{creationError}</p>
              )}
              <Sheet open={isMobile && mobileSettingsOpen} onOpenChange={setMobileSettingsOpen}>
                <SheetContent
                  side="bottom"
                  hideCloseButton
                  className="rounded-t-[1.75rem] border-border/70 px-4 pb-6 pt-5 sm:max-w-none"
                >
                  <SheetHeader className="mb-4">
                    <SheetTitle className="text-base">Session settings</SheetTitle>
                    <SheetDescription>Pick the repo, branch, model, and reasoning for this run.</SheetDescription>
                  </SheetHeader>
                  {settingsControls}
                  <Button type="button" className="mt-5 h-11 w-full rounded-xl" onClick={() => setMobileSettingsOpen(false)}>
                    Done
                  </Button>
                </SheetContent>
              </Sheet>
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  );
}
