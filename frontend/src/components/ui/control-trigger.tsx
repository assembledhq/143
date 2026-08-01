import * as React from "react";

import { Button } from "@/components/ui/button";
import {
  controlHeightVariants,
  type ControlDensity,
} from "@/components/ui/control-sizing";
import { cn } from "@/lib/utils";

type ControlTriggerProps = Omit<React.ComponentProps<typeof Button>, "size"> & {
  density?: ControlDensity;
};

function ControlTrigger({
  className,
  density = "default",
  ...props
}: ControlTriggerProps) {
  return (
    <Button
      data-slot="control-trigger"
      data-density={density}
      className={cn(controlHeightVariants({ density }), className)}
      {...props}
    />
  );
}

export { ControlTrigger };
export type { ControlTriggerProps };
