"use client";

import { useDeferredValue, useEffect, useMemo, useRef, useState } from "react";
import { FileCode2, FolderTree } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { SessionComposerTriggerPicker, flattenGroups, type TriggerPickerGroup, type TriggerPickerPosition } from "@/components/session-composer-trigger-picker";
import { Textarea } from "@/components/ui/textarea";
import { useSessionComposerSlashCommands } from "@/hooks/use-session-composer-slash-commands";
import { api } from "@/lib/api";
import {
  COMPOSER_TRIGGER_SPECS,
  findActiveTrigger,
  insertCommandAtCaret,
  insertMentionAtCaret,
} from "@/lib/session-composer-mentions";
import { queryKeys } from "@/lib/query-keys";
import type { ListResponse, SessionInputReference } from "@/lib/types";
import { cn } from "@/lib/utils";

const triggerPickerIconClassName = "h-4 w-4 shrink-0";
const directoryTriggerIcon = <FolderTree className={triggerPickerIconClassName} />;
const fileTriggerIcon = <FileCode2 className={triggerPickerIconClassName} />;

type AutomationGoalEditorProps = {
  id: string;
  value: string;
  onChange: (value: string) => void;
  repositoryId?: string;
  branch?: string;
  agentType: string;
  placeholder?: string;
  rows?: number;
  disabled?: boolean;
  ariaInvalid?: boolean;
  className?: string;
};

export function AutomationGoalEditor({
  id,
  value,
  onChange,
  repositoryId,
  branch,
  agentType,
  placeholder,
  rows = 3,
  disabled = false,
  ariaInvalid = false,
  className,
}: AutomationGoalEditorProps) {
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const [caretPosition, setCaretPosition] = useState(0);
  const [selectedTriggerIndex, setSelectedTriggerIndex] = useState(0);
  const [triggerDismissed, setTriggerDismissed] = useState(false);
  const [pickerPosition, setPickerPosition] = useState<TriggerPickerPosition | null>(null);

  const activeTrigger = useMemo(
    () => findActiveTrigger(value, caretPosition, COMPOSER_TRIGGER_SPECS),
    [value, caretPosition],
  );
  const activeMention = activeTrigger?.trigger === "@" ? activeTrigger : null;
  const activeCommand = activeTrigger?.trigger === "/" ? activeTrigger : null;
  const deferredMentionQuery = useDeferredValue(activeMention?.query ?? "");
  const deferredCommandQuery = useDeferredValue(activeCommand?.query ?? "");

  const { data: fileMentionsResponse, isFetching: fileMentionsLoading } = useQuery<ListResponse<SessionInputReference>>({
    queryKey: queryKeys.sessionComposer.files(repositoryId ?? "", branch ?? "", deferredMentionQuery),
    queryFn: () => api.sessionComposer.files(repositoryId ?? "", branch ?? "", deferredMentionQuery),
    enabled: !!repositoryId && activeMention !== null && !triggerDismissed,
    staleTime: 30 * 1000,
  });
  const fileMentions = useMemo(() => fileMentionsResponse?.data ?? [], [fileMentionsResponse]);

  const slashCommandsQuery = useSessionComposerSlashCommands({
    agentType,
    query: deferredCommandQuery,
    repositoryId,
    branch,
    enabled: activeCommand !== null && !triggerDismissed,
  });
  const slashCommandGroups = useMemo(() => slashCommandsQuery.data?.groups ?? [], [slashCommandsQuery.data]);
  const slashCommandItems = useMemo(
    () => slashCommandGroups.flatMap((group) => group.items),
    [slashCommandGroups],
  );

  const showMentionPicker = !!repositoryId && activeMention !== null && !triggerDismissed;
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
  }, [fileMentions, showCommandPicker, showMentionPicker, slashCommandGroups]);
  const flattenedPickerItems = useMemo(() => flattenGroups(pickerGroups), [pickerGroups]);
  const pickerOpen = showMentionPicker || showCommandPicker;
  const pickerLoading = showMentionPicker
    ? fileMentionsLoading
    : showCommandPicker
      ? slashCommandsQuery.isFetching
      : false;
  const pickerEmptyLabel = showCommandPicker
    ? `No commands for /${activeCommand?.query ?? ""}`
    : `No matches for @${activeMention?.query ?? ""}`;

  useEffect(() => {
    if (!pickerOpen) {
      return;
    }

    function updatePickerPosition() {
      const container = containerRef.current;
      if (!container) {
        return;
      }

      const rect = container.getBoundingClientRect();
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

    const container = containerRef.current;
    const resizeObserver = container && typeof ResizeObserver !== "undefined"
      ? new ResizeObserver(() => {
        updatePickerPosition();
      })
      : null;
    if (container && resizeObserver) {
      resizeObserver.observe(container);
    }

    return () => {
      window.removeEventListener("resize", updatePickerPosition);
      window.removeEventListener("scroll", updatePickerPosition, true);
      resizeObserver?.disconnect();
    };
  }, [pickerOpen, fileMentions.length, fileMentionsLoading, slashCommandItems.length]);

  function updateCaret(nextCaret: number) {
    setCaretPosition(nextCaret);
    setTriggerDismissed(false);
    setSelectedTriggerIndex(0);
  }

  function applyMention(index: number) {
    if (!activeMention || !textareaRef.current) {
      return;
    }
    const reference = fileMentions[index];
    if (!reference) {
      return;
    }

    const inserted = insertMentionAtCaret(value, activeMention, reference);
    onChange(inserted.text);
    setCaretPosition(inserted.caret);
    setTriggerDismissed(false);

    requestAnimationFrame(() => {
      const textarea = textareaRef.current;
      if (!textarea) {
        return;
      }
      textarea.focus();
      textarea.setSelectionRange(inserted.caret, inserted.caret);
    });
  }

  function applyCommand(index: number) {
    if (!activeCommand || !textareaRef.current) {
      return;
    }
    const command = slashCommandItems[index];
    if (!command) {
      return;
    }

    const inserted = insertCommandAtCaret(value, activeCommand, command);
    onChange(inserted.text);
    setCaretPosition(inserted.caret);
    setTriggerDismissed(false);

    requestAnimationFrame(() => {
      const textarea = textareaRef.current;
      if (!textarea) {
        return;
      }
      textarea.focus();
      textarea.setSelectionRange(inserted.caret, inserted.caret);
    });
  }

  return (
    <div ref={containerRef} className="relative">
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
          if (flatIndex < 0) {
            return;
          }
          if (showMentionPicker) {
            applyMention(flatIndex);
            return;
          }
          if (showCommandPicker) {
            applyCommand(flatIndex);
          }
        }}
      />

      <Textarea
        ref={textareaRef}
        id={id}
        value={value}
        onChange={(event) => {
          onChange(event.target.value);
          updateCaret(event.target.selectionStart ?? event.target.value.length);
        }}
        onClick={(event) => updateCaret(event.currentTarget.selectionStart ?? value.length)}
        onKeyUp={(event) => updateCaret(event.currentTarget.selectionStart ?? value.length)}
        onSelect={(event) => updateCaret(event.currentTarget.selectionStart ?? value.length)}
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
              if (showMentionPicker) {
                applyMention(selectedTriggerIndex);
                return;
              }
              if (showCommandPicker) {
                applyCommand(selectedTriggerIndex);
                return;
              }
            }
          }
          if (pickerOpen && event.key === "Escape") {
            event.preventDefault();
            setTriggerDismissed(true);
          }
        }}
        placeholder={placeholder}
        rows={rows}
        disabled={disabled}
        aria-invalid={ariaInvalid}
        className={cn(className)}
      />
    </div>
  );
}
