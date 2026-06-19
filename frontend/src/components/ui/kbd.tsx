import { cn } from "@/lib/utils";

type KbdVariant = "default" | "inverted" | "primary";

const variantClasses: Record<KbdVariant, string> = {
  // On light surfaces (cards, panels, inputs).
  default:
    "border-border bg-muted/50 text-muted-foreground",
  // On inverted surfaces (tooltips use bg-foreground/text-background).
  inverted:
    "border-background/25 bg-background/15 text-background/85",
  // On solid primary / gradient buttons.
  primary:
    "border-white/25 bg-white/15 text-white/90",
};

export function Kbd({
  variant = "default",
  className,
  children,
}: {
  variant?: KbdVariant;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    // Shortcut hints are visual affordances; keyboard users discover the
    // shortcuts via the help overlay, so keep hints out of accessible names.
    <kbd
      aria-hidden="true"
      className={cn(
        "pointer-events-none inline-flex h-5 min-w-5 select-none items-center justify-center gap-0.5 rounded border px-1 font-mono text-xs font-medium",
        variantClasses[variant],
        className,
      )}
    >
      {children}
    </kbd>
  );
}
