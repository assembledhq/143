"use client";

import { useEffect, useRef } from "react";
import { X } from "lucide-react";

interface KeyboardHelpOverlayProps {
  open: boolean;
  onClose: () => void;
}

const shortcuts = [
  { key: "j / k", action: "Next / previous file" },
  { key: "n / p", action: "Next / previous hunk" },
  { key: "Enter", action: "Jump to selected file in diff" },
  { key: "c", action: "Add comment on current file" },
  { key: "x", action: "Expand context around cursor" },
  { key: "f", action: "Toggle file tree panel" },
  { key: "u", action: "Unified diff view" },
  { key: "s", action: "Split diff view" },
  { key: "e", action: "Toggle repository explorer" },
  { key: "m", action: "Back to conversation" },
  { key: "Esc", action: "Back to conversation" },
  { key: "?", action: "Show this help" },
];

export function KeyboardHelpOverlay({ open, onClose }: KeyboardHelpOverlayProps) {
  const overlayRef = useRef<HTMLDivElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;

    // Focus the dialog when it opens
    dialogRef.current?.focus();

    function handleKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape" || e.key === "?") {
        e.preventDefault();
        onClose();
      }
      // Trap focus within the dialog
      if (e.key === "Tab") {
        const focusable = dialogRef.current?.querySelectorAll<HTMLElement>(
          'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'
        );
        if (!focusable || focusable.length === 0) return;
        const first = focusable[0];
        const last = focusable[focusable.length - 1];
        if (e.shiftKey && document.activeElement === first) {
          e.preventDefault();
          last.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault();
          first.focus();
        }
      }
    }

    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [open, onClose]);

  if (!open) return null;

  return (
    <div
      ref={overlayRef}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40"
      role="presentation"
      onClick={(e) => {
        if (e.target === overlayRef.current) onClose();
      }}
    >
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label="Keyboard shortcuts"
        tabIndex={-1}
        className="bg-surface-raised border border-border rounded-lg shadow-lg w-[360px] max-w-[90vw] outline-none"
      >
        <div className="flex items-center justify-between px-4 py-3 border-b border-border">
          <h2 className="text-sm font-semibold">Keyboard shortcuts</h2>
          <button
            onClick={onClose}
            aria-label="Close keyboard shortcuts"
            className="h-6 w-6 flex items-center justify-center rounded hover:bg-surface-hover transition-colors"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        </div>
        <div className="p-4">
          <table className="w-full text-xs">
            <tbody>
              {shortcuts.map((s) => (
                <tr key={s.key} className="border-b border-border/30 last:border-0">
                  <td className="py-1.5 pr-4">
                    <kbd className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded border border-border bg-muted/50 font-mono text-xs text-muted-foreground">
                      {s.key}
                    </kbd>
                  </td>
                  <td className="py-1.5 text-muted-foreground">{s.action}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
