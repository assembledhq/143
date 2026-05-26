import type { BaseLayoutProps } from "fumadocs-ui/layouts/shared";
import { BookOpen, Github } from "lucide-react";
import { DocsThemeSwitch } from "@/components/docs/docs-theme-switch";

export function docsLayoutOptions(): BaseLayoutProps {
  return {
    nav: {
      title: (
        <span className="inline-flex items-center gap-2 font-semibold">
          <span className="flex size-6 items-center justify-center rounded-md border border-border bg-card">
            <BookOpen className="size-3.5" aria-hidden="true" />
          </span>
          143 docs
        </span>
      ),
      url: "/docs",
      transparentMode: "none",
    },
    links: [
      {
        text: "Get started",
        url: "/docs/getting-started",
        active: "nested-url",
        on: "nav",
      },
      {
        text: "Guides",
        url: "/docs/guides",
        active: "nested-url",
        on: "nav",
      },
      {
        text: "Self-hosting",
        url: "/docs/self-hosting",
        active: "nested-url",
        on: "nav",
      },
      {
        text: "Reference",
        url: "/docs/reference",
        active: "nested-url",
        on: "nav",
      },
      {
        type: "icon",
        text: "GitHub",
        label: "Open GitHub repository",
        url: "https://github.com/assembledhq/143",
        external: true,
        icon: <Github className="size-4" aria-hidden="true" />,
      },
    ],
    slots: {
      themeSwitch: DocsThemeSwitch,
    },
    themeSwitch: {
      enabled: true,
    },
    searchToggle: {
      enabled: true,
    },
  };
}
