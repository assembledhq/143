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
        "font-medium text-primary underline decoration-primary/40 underline-offset-4 transition-colors hover:decoration-primary focus-visible:rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        className,
      )}
      target={target}
      rel={rel}
      {...props}
    >
      {children}
      {"\u00a0"}
      <ExternalLinkIcon
        data-slot="external-link-icon"
        aria-hidden="true"
        className="inline-block size-3.5 translate-y-px"
      />
    </a>
  )
}

export { ExternalLink }
