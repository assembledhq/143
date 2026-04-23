import { Toaster } from "sonner";
import { cn } from "@/lib/utils";
import { errorSurfaceClassNames } from "./error-styles";

const toastBaseClassName =
  "pointer-events-auto flex w-full items-start gap-3 rounded-xl p-4 shadow-xl backdrop-blur-sm";
const toastDefaultClassName = "border border-border/70 bg-background/95 text-foreground";
const toastSuccessClassName = "border border-emerald-500/20 bg-emerald-500/[0.08] text-foreground";
const toastWarningClassName = "border border-amber-500/25 bg-amber-500/[0.08] text-foreground";
const toastInfoClassName = "border border-sky-500/20 bg-sky-500/[0.08] text-foreground";

export function AppToaster() {
  return (
    <Toaster
      position="bottom-right"
      expand
      closeButton
      toastOptions={{
        unstyled: true,
        classNames: {
          toast: toastBaseClassName,
          default: toastDefaultClassName,
          success: toastSuccessClassName,
          info: toastInfoClassName,
          warning: toastWarningClassName,
          loading: toastDefaultClassName,
          error: cn(errorSurfaceClassNames.container, "shadow-xl"),
          content: "min-w-0 flex-1 space-y-1",
          title: "text-sm font-medium leading-5 text-foreground",
          description: "text-sm leading-5 text-muted-foreground",
          actionButton:
            "inline-flex h-7 shrink-0 items-center justify-center rounded-md border border-border/70 bg-background/85 px-2.5 text-xs font-medium text-foreground transition-colors hover:border-primary/30 hover:bg-background",
          cancelButton:
            "inline-flex h-7 shrink-0 items-center justify-center rounded-md border border-border/70 bg-background/85 px-2.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-background hover:text-foreground",
          closeButton:
            "border border-border/70 bg-background/85 text-muted-foreground transition-colors hover:bg-background hover:text-foreground",
        },
      }}
    />
  );
}
