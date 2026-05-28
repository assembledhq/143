"use client";

import { useState, useRef, useEffect } from "react";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

interface CommentInputProps {
  onSubmit: (body: string) => void;
  onCancel: () => void;
  /** Pre-fill for editing an existing comment */
  initialValue?: string;
  submitLabel?: string;
  autoFocus?: boolean;
  className?: string;
}

export function CommentInput({
  onSubmit,
  onCancel,
  initialValue = "",
  submitLabel = "Add comment",
  autoFocus = true,
  className,
}: CommentInputProps) {
  const [value, setValue] = useState(initialValue);
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  // Sync value when initialValue changes (e.g. switching to edit a different comment)
  useEffect(() => {
    setValue(initialValue);
  }, [initialValue]);

  useEffect(() => {
    if (autoFocus && textareaRef.current) {
      textareaRef.current.focus();
    }
  }, [autoFocus]);

  // Auto-resize
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 160)}px`;
  }, [value]);

  function handleKeyDown(e: React.KeyboardEvent) {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      if (value.trim()) {
        onSubmit(value.trim());
      }
    } else if (e.key === "Escape") {
      e.preventDefault();
      onCancel();
    }
  }

  return (
    <div
      data-testid="inline-comment-composer"
      className={cn(
        "my-1.5 w-full overflow-hidden rounded-md border border-border bg-surface-raised shadow-sm",
        className
      )}
    >
      <Textarea
        ref={textareaRef}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={handleKeyDown}
        placeholder="Leave a comment..."
        aria-label="Review comment"
        className="min-h-[60px] max-h-[160px] resize-none rounded-t-md rounded-b-none border-0 border-b px-3 py-2 bg-transparent focus-visible:ring-0 focus-visible:ring-offset-0 placeholder:text-muted-foreground/50"
      />
      <div className="flex flex-wrap items-center justify-end gap-2 border-t border-border/50 bg-surface-pane/70 px-3 py-2 rounded-b-md">
        <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={onCancel}>
          Cancel
        </Button>
        <Button
          size="sm"
          className="h-7 text-xs"
          disabled={!value.trim()}
          onClick={() => onSubmit(value.trim())}
        >
          {submitLabel}
          <kbd className="ml-1.5 hidden text-xs opacity-60 sm:inline">
            {typeof navigator !== "undefined" && /mac/i.test(navigator.userAgent) ? "⌘" : "Ctrl"}↵
          </kbd>
        </Button>
      </div>
    </div>
  );
}
