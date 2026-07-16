import * as React from "react"
import { cva } from "class-variance-authority"

import { cn } from "@/lib/utils"
import { ButtonGroupSizeContext, type ButtonGroupSize } from "./button-group-context"

const buttonGroupVariants = cva("inline-flex w-fit items-stretch")

function ButtonGroup({
  className,
  size = "default",
  ...props
}: React.ComponentProps<"div"> & { size?: ButtonGroupSize }) {
  const resolvedSize = size

  return (
    <ButtonGroupSizeContext.Provider value={resolvedSize}>
      <div
        role="group"
        data-slot="button-group"
        data-size={resolvedSize}
        className={cn(buttonGroupVariants(), className)}
        {...props}
      />
    </ButtonGroupSizeContext.Provider>
  )
}

export { ButtonGroup, buttonGroupVariants }
