import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"
import { Slot } from "radix-ui"

import { cn } from "@/lib/utils"

const badgeVariants = cva(
  "inline-flex items-center justify-center rounded-full border border-transparent px-2 py-0.5 text-xs leading-4 font-medium w-fit whitespace-nowrap shrink-0 [&>svg]:size-3 gap-1 [&>svg]:pointer-events-none focus-visible:border-ring focus-visible:ring-ring/30 focus-visible:ring-2 aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive transition-[color,background-color,border-color] overflow-hidden",
  {
    variants: {
      variant: {
        default: "bg-primary/12 text-primary border-primary/18 [a&]:hover:bg-primary/18",
        secondary:
          "bg-secondary text-secondary-foreground border-border/50 [a&]:hover:bg-secondary/90",
        destructive:
          "bg-destructive/12 text-destructive border-destructive/18 [a&]:hover:bg-destructive/18 focus-visible:ring-destructive/20",
        outline:
          "border-border text-foreground [a&]:hover:bg-accent [a&]:hover:text-accent-foreground",
        ghost: "[a&]:hover:bg-accent [a&]:hover:text-accent-foreground",
        link: "text-primary underline-offset-4 [a&]:hover:underline",
        glow: "bg-primary/12 text-primary border-primary/18",
        success: "bg-success/12 text-success border-success/18",
        warning: "bg-warning/12 text-warning border-warning/18",
        info: "bg-info/12 text-info border-info/18",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  }
)

function Badge({
  className,
  variant = "default",
  asChild = false,
  ...props
}: React.ComponentProps<"span"> &
  VariantProps<typeof badgeVariants> & { asChild?: boolean }) {
  const Comp = asChild ? Slot.Root : "span"

  return (
    <Comp
      data-slot="badge"
      data-variant={variant}
      className={cn(badgeVariants({ variant }), className)}
      {...props}
    />
  )
}

export { Badge, badgeVariants }
