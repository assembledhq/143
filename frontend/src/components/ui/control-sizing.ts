import { cva } from "class-variance-authority";

const controlDensities = ["default", "compact", "dense"] as const;
type ControlDensity = (typeof controlDensities)[number];

const controlDensityClasses = {
  default: "h-11 sm:h-9",
  compact: "h-11 sm:h-8",
  dense: "h-11 sm:h-7",
} satisfies Record<ControlDensity, string>;

const controlHeightVariants = cva("min-h-11 max-sm:text-base sm:min-h-0", {
  variants: {
    density: controlDensityClasses,
  },
  defaultVariants: {
    density: "default",
  },
});

export { controlDensities, controlHeightVariants };
export type { ControlDensity };
