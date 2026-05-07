"use client";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

type SessionKeyboardHelpOverlayProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
};

const shortcutGroups = [
  {
    title: "Navigate sessions",
    items: [
      ["j / k", "Next / previous session"],
      ["Enter", "Open active session"],
      ["/", "Focus session search"],
      ["n", "New session"],
      ["Shift+A", "Archive active session"],
    ],
  },
  {
    title: "Read transcript",
    items: [
      ["Arrow keys", "Scroll conversation"],
      ["Page Up / Down", "Scroll by page"],
      ["End / .", "Jump to latest"],
      ["i", "Focus follow-up composer"],
      ["Esc", "Close details or leave empty composer"],
    ],
  },
  {
    title: "Tabs and details",
    items: [
      ["[ / ]", "Previous / next agent tab"],
      ["Ctrl+Tab", "Next agent tab"],
      ["t", "Add agent tab"],
      ["d", "Toggle session details"],
      ["Shift+[ / ]", "Previous / next detail tab"],
      ["r", "Open review diff"],
    ],
  },
  {
    title: "Ship PR",
    items: [
      ["p c", "Create or retry PR"],
      ["p v", "View PR"],
      ["p p", "Push changes"],
      ["p t", "Fix tests"],
      ["p r", "Resolve conflicts"],
      ["p m", "Merge PR"],
    ],
  },
];

export function SessionKeyboardHelpOverlay({ open, onOpenChange }: SessionKeyboardHelpOverlayProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        aria-label="Session keyboard shortcuts"
        className="max-h-[min(720px,90vh)] gap-0 overflow-hidden p-0 sm:max-w-2xl"
      >
        <DialogHeader className="border-b border-border px-5 py-4">
          <DialogTitle className="text-base">Session keyboard shortcuts</DialogTitle>
          <DialogDescription>
            Session shell shortcuts are ignored while typing or while menus and dialogs are open.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-4 overflow-y-auto p-5 sm:grid-cols-2">
          {shortcutGroups.map((group) => (
            <section key={group.title} className="space-y-2">
              <h3 className="text-xs font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                {group.title}
              </h3>
              <div className="space-y-1.5">
                {group.items.map(([key, description]) => (
                  <div key={`${group.title}:${key}`} className="grid grid-cols-[6.5rem_1fr] items-center gap-3 text-xs">
                    <kbd className="inline-flex min-h-6 items-center justify-center rounded-md border border-border bg-muted/50 px-2 font-mono text-muted-foreground">
                      {key}
                    </kbd>
                    <span className="text-muted-foreground">{description}</span>
                  </div>
                ))}
              </div>
            </section>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  );
}
