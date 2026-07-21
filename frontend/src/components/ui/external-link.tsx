import * as React from "react"
import { ExternalLink as ExternalLinkIcon } from "lucide-react"

import { cn } from "@/lib/utils"

function ExternalLink({
  className,
  children,
  target = "_blank",
  rel = "noopener noreferrer",
  ...props
}: React.ComponentProps<"a">) {
  return (
    <a
      data-slot="external-link"
      className={cn(
        "inline-flex items-baseline gap-1 font-medium text-primary underline decoration-primary/40 underline-offset-4 transition-colors hover:decoration-primary focus-visible:rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        className,
      )}
      target={target}
      rel={rel}
      {...props}
    >
      <span>{children}</span>
      <ExternalLinkIcon
        data-slot="external-link-icon"
        aria-hidden="true"
        className="size-3.5 shrink-0 translate-y-px"
      />
    </a>
  )
}

export { ExternalLink }
