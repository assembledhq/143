import * as React from "react"

import { cn } from "@/lib/utils"
import { controlHeightVariants, type ControlDensity } from "./control-sizing"

function Input({
  className,
  type,
  density = "default",
  inset = "default",
  ...props
}: React.ComponentProps<"input"> & {
  density?: ControlDensity
  inset?: "default" | "none"
}) {
  return (
    <input
      type={type}
      data-slot="input"
      data-density={density}
      data-inset={inset}
      className={cn(
        "file:text-foreground placeholder:text-muted-foreground selection:bg-primary selection:text-primary-foreground border-border-strong w-full min-w-0 rounded-md border bg-surface-raised type-dense transition-[color,border-color,box-shadow] duration-[125ms] ease-[cubic-bezier(0.16,1,0.3,1)] outline-none file:inline-flex file:h-6 file:border-0 file:bg-transparent file:text-xs file:font-medium disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50",
        inset === "none"
          ? "px-0 py-0"
          : cn("px-2.5", density === "dense" ? "py-1" : "py-1.5"),
        controlHeightVariants({ density }),
        "focus-visible:border-ring focus-visible:ring-ring/18 focus-visible:ring-2",
        "aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive",
        className
      )}
      {...props}
    />
  )
}

export { Input }
