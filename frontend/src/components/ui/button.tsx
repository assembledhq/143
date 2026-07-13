import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"
import { Loader2 } from "lucide-react"
import { Slot } from "radix-ui"

import { cn } from "@/lib/utils"

const buttonVariants = cva(
  "inline-flex cursor-pointer items-center justify-center gap-1.5 whitespace-nowrap rounded-md type-dense font-medium transition-[color,background-color,border-color,box-shadow,transform] duration-[125ms] ease-[cubic-bezier(0.16,1,0.3,1)] disabled:pointer-events-none disabled:opacity-50 disabled:data-[loading=true]:opacity-100 [&_svg]:pointer-events-none [&_svg:not([class*='size-'])]:size-4 shrink-0 [&_svg]:shrink-0 outline-none focus-visible:border-ring focus-visible:ring-ring/25 focus-visible:ring-2 aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground shadow-[0_1px_0_rgb(0_0_0_/_12%)] hover:bg-primary/90 active:scale-[0.985]",
        destructive:
          "bg-destructive text-white shadow-sm hover:bg-destructive/90 focus-visible:ring-destructive/20 dark:focus-visible:ring-destructive/40 dark:bg-destructive/60",
        outline:
          "border border-border-strong bg-surface-raised text-foreground hover:border-primary/35 hover:bg-accent/55 hover:text-foreground dark:bg-surface-raised",
        secondary:
          "bg-secondary text-secondary-foreground hover:bg-secondary/80",
        ghost:
          "hover:bg-accent hover:text-accent-foreground dark:hover:bg-accent/50",
        link: "text-primary underline-offset-4 hover:underline",
      },
      size: {
        default: "h-11 px-3 py-1.5 sm:h-8 has-[>svg]:px-2.5",
        xs: "h-11 gap-1 rounded-md px-2 text-xs sm:h-6 has-[>svg]:px-1.5 [&_svg:not([class*='size-'])]:size-3",
        sm: "h-11 rounded-md gap-1 px-2.5 sm:h-7 has-[>svg]:px-2",
        lg: "h-11 px-5 sm:h-9 has-[>svg]:px-3.5",
        icon: "size-11 sm:size-8",
        "icon-xs": "size-11 rounded-md sm:size-6 [&_svg:not([class*='size-'])]:size-3",
        "icon-sm": "size-11 sm:size-7",
        "icon-lg": "size-11 sm:size-9",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
)

function Button({
  className,
  variant = "default",
  size = "default",
  asChild = false,
  loading = false,
  disabled,
  children,
  ...props
}: React.ComponentProps<"button"> &
  VariantProps<typeof buttonVariants> & {
    asChild?: boolean
    loading?: boolean
  }) {
  const Comp = asChild ? Slot.Root : "button"
  const isDisabled = loading || disabled
  const showSpinner = loading && !asChild
  const content = showSpinner ? (
    <>
      <Loader2
        data-slot="button-spinner"
        className="size-4 animate-spin"
        aria-hidden="true"
      />
      {children}
    </>
  ) : (
    children
  )

  return (
    <Comp
      data-slot="button"
      data-variant={variant}
      data-size={size}
      data-loading={loading ? "true" : undefined}
      className={cn(buttonVariants({ variant, size, className }), isDisabled && "pointer-events-none")}
      disabled={!asChild ? isDisabled : undefined}
      aria-disabled={isDisabled || undefined}
      {...props}
    >{content}</Comp>
  )
}

export { Button, buttonVariants }
