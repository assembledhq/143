"use client";

import { useState, useRef, useEffect } from "react";
import { Button } from "@/components/ui/button";

interface CommentInputProps {
  onSubmit: (body: string) => void;
  onCancel: () => void;
  /** Pre-fill for editing an existing comment */
  initialValue?: string;
  submitLabel?: string;
  autoFocus?: boolean;
}

export function CommentInput({
  onSubmit,
  onCancel,
  initialValue = "",
  submitLabel = "Add comment",
  autoFocus = true,
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
    <div className="mx-2 my-1.5 rounded-md border border-border bg-background shadow-sm">
      <textarea
        ref={textareaRef}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={handleKeyDown}
        placeholder="Leave a comment..."
        aria-label="Review comment"
        className="w-full min-h-[60px] max-h-[160px] resize-none rounded-t-md px-3 py-2 text-[13px] bg-transparent focus:outline-none placeholder:text-muted-foreground/50"
      />
      <div className="flex items-center justify-end gap-2 px-3 py-2 border-t border-border/50 bg-muted/20 rounded-b-md">
        <Button variant="ghost" size="sm" className="h-7 text-[12px]" onClick={onCancel}>
          Cancel
        </Button>
        <Button
          size="sm"
          className="h-7 text-[12px]"
          disabled={!value.trim()}
          onClick={() => onSubmit(value.trim())}
        >
          {submitLabel}
          <kbd className="ml-1.5 text-[10px] opacity-60">
            {typeof navigator !== "undefined" && /mac/i.test(navigator.userAgent) ? "⌘" : "Ctrl"}↵
          </kbd>
        </Button>
      </div>
    </div>
  );
}
