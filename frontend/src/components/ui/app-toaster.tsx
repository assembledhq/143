import { Toaster } from "sonner";

export function AppToaster() {
  return (
    <Toaster
      position="bottom-right"
      expand={false}
      closeButton={false}
      visibleToasts={4}
      toastOptions={{
        unstyled: true,
        classNames: {
          toast:
            "pointer-events-auto border-0 bg-transparent p-0 shadow-none [&[data-removed=true]]:shadow-none",
          content: "contents",
        },
      }}
    />
  );
}
