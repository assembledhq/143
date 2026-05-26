"use client";

import { ThemeSwitch, type ThemeSwitchProps } from "fumadocs-ui/layouts/shared/slots/theme-switch";

export function DocsThemeSwitch({ className: _className, ...props }: ThemeSwitchProps) {
  void _className;

  return (
    <ThemeSwitch
      {...props}
      className="h-8 rounded-lg border border-border bg-background p-0.5 shadow-sm *:rounded-md"
    />
  );
}
