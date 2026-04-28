import type { ReactNode } from "react";
import { toast as sonnerToast, type ExternalToast } from "sonner";
import { ToastCard } from "@/components/ui/toast-card";

interface NotifyAction {
  label: ReactNode;
  onClick: () => void;
}

interface NotifyOptions
  extends Omit<
    ExternalToast,
    "action" | "cancel" | "closeButton" | "description" | "title" | "jsx"
  > {
  description?: ReactNode;
  action?: NotifyAction;
  showDismiss?: boolean;
}

type NotifyVariant = "success" | "info" | "warning" | "error";

const defaultDurationMs: Record<NotifyVariant, number> = {
  success: 3200,
  info: 4200,
  warning: 6000,
  error: 10000,
};

const defaultShowDismiss: Record<NotifyVariant, boolean> = {
  success: false,
  info: false,
  warning: true,
  error: true,
};

function showToast(
  variant: NotifyVariant,
  title: ReactNode,
  options?: NotifyOptions,
) {
  const {
    action,
    description,
    duration,
    showDismiss,
    ...sonnerOptions
  } = options ?? {};

  return sonnerToast.custom(
    (toastId) => (
      <ToastCard
        variant={variant}
        title={title}
        description={description}
        action={action}
        onDismiss={
          showDismiss ?? defaultShowDismiss[variant]
            ? () => {
                sonnerToast.dismiss(toastId);
              }
            : undefined
        }
      />
    ),
    {
      ...sonnerOptions,
      closeButton: false,
      duration: duration ?? defaultDurationMs[variant],
    },
  );
}

export const notify = {
  success(title: ReactNode, options?: NotifyOptions) {
    return showToast("success", title, options);
  },
  info(title: ReactNode, options?: NotifyOptions) {
    return showToast("info", title, options);
  },
  warning(title: ReactNode, options?: NotifyOptions) {
    return showToast("warning", title, options);
  },
  error(title: ReactNode, options?: NotifyOptions) {
    return showToast("error", title, options);
  },
  dismiss(id?: string | number) {
    return sonnerToast.dismiss(id);
  },
};
