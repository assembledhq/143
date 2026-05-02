import Image from "next/image";
import { cn } from "@/lib/utils";
import { getIntegrationByKey } from "@/lib/integrations";

export function LinearIcon({ className }: { className?: string }) {
  const linear = getIntegrationByKey("linear");
  return (
    <Image
      src={linear.logoSrc}
      alt=""
      aria-hidden="true"
      width={16}
      height={16}
      unoptimized
      className={cn("object-contain dark:invert", className)}
    />
  );
}
