import type { ComponentProps } from "react";

import { Card } from "@/components/ui/card";

export function InteractiveCard(props: ComponentProps<typeof Card>) {
  return <Card variant="interactive" {...props} />;
}
