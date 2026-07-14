import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"

import { cn } from "@/lib/utils"

const buttonGroupVariants = cva(
  "inline-flex w-fit items-stretch [&_[data-slot=button]]:!h-full",
  {
    variants: {
      size: {
        default: "h-10 sm:h-8",
        xs: "h-10 sm:h-6",
        sm: "h-10 sm:h-7",
        lg: "h-10 sm:h-9",
      },
    },
    defaultVariants: {
      size: "default",
    },
  },
)

function ButtonGroup({
  className,
  size = "default",
  ...props
}: React.ComponentProps<"div"> & VariantProps<typeof buttonGroupVariants>) {
  return (
    <div
      role="group"
      data-slot="button-group"
      data-size={size}
      className={cn(buttonGroupVariants({ size }), className)}
      {...props}
    />
  )
}

export { ButtonGroup, buttonGroupVariants }
