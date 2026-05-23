import type { BaseLayoutProps } from "fumadocs-ui/layouts/shared";
import { BookOpen, Github } from "lucide-react";

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
        text: "Guides",
        url: "/docs/guides",
        active: "nested-url",
      },
      {
        text: "Self-hosting",
        url: "/docs/self-hosting",
        active: "nested-url",
      },
      {
        text: "Reference",
        url: "/docs/reference",
        active: "nested-url",
      },
      {
        type: "icon",
        text: "GitHub",
        label: "Open GitHub repository",
        url: "https://github.com/143",
        external: true,
        icon: <Github className="size-4" aria-hidden="true" />,
      },
    ],
    themeSwitch: {
      enabled: true,
    },
    searchToggle: {
      enabled: true,
    },
  };
}
