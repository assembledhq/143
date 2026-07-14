import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"

import { cn } from "@/lib/utils"
import { ButtonGroupSizeContext, type ButtonGroupSize } from "./button-group-context"

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
  const resolvedSize: ButtonGroupSize = size ?? "default"

  return (
    <ButtonGroupSizeContext.Provider value={resolvedSize}>
      <div
        role="group"
        data-slot="button-group"
        data-size={resolvedSize}
        className={cn(buttonGroupVariants({ size: resolvedSize }), className)}
        {...props}
      />
    </ButtonGroupSizeContext.Provider>
  )
}

export { ButtonGroup, buttonGroupVariants }
