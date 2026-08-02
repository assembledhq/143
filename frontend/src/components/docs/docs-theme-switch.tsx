"use client";

import type { ThemeSwitchProps } from "fumadocs-ui/layouts/shared/slots/theme-switch";
import { Monitor, Moon, Sun } from "lucide-react";
import { useTheme } from "next-themes";
import { useSyncExternalStore } from "react";
import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

const themeOptions = [
  { value: "light", label: "Light theme", icon: Sun },
  { value: "dark", label: "Dark theme", icon: Moon },
  { value: "system", label: "Use system theme", icon: Monitor },
] as const;

const emptySubscribe = () => () => {};

export function DocsThemeSwitch({
  className,
  mode = "light-dark",
  ...props
}: ThemeSwitchProps) {
  const { resolvedTheme, setTheme, theme } = useTheme();
  const mounted = useSyncExternalStore(emptySubscribe, () => true, () => false);
  const options = mode === "light-dark-system" ? themeOptions : themeOptions.slice(0, 2);
  const selectedTheme = mounted
    ? mode === "light-dark-system"
      ? theme
      : resolvedTheme
    : undefined;

  return (
    <TooltipProvider>
      <div
        {...props}
        role="group"
        aria-label="Theme"
        className={cn(
          "inline-flex items-center rounded-lg border border-border bg-background p-0.5 shadow-sm",
          className,
        )}
      >
        {options.map(({ value, label, icon: Icon }) => {
          const isSelected = selectedTheme === value;

          return (
            <Tooltip key={value}>
              <TooltipTrigger asChild>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-sm"
                  aria-label={label}
                  aria-pressed={isSelected}
                  onClick={() => setTheme(value)}
                  className={cn(
                    "size-11 touch-manipulation rounded-md text-muted-foreground shadow-none active:scale-[0.92] sm:size-11 md:size-7",
                    isSelected && "bg-accent text-foreground",
                  )}
                >
                  <Icon
                    className="size-4"
                    fill={isSelected ? "currentColor" : "none"}
                    aria-hidden="true"
                  />
                </Button>
              </TooltipTrigger>
              <TooltipContent side="bottom" sideOffset={6}>{label}</TooltipContent>
            </Tooltip>
          );
        })}
      </div>
    </TooltipProvider>
  );
}
