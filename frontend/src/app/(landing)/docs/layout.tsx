import { DocsLayout } from "fumadocs-ui/layouts/docs";
import type { ReactNode } from "react";
import { docsLayoutOptions } from "@/lib/docs/layout";
import { source } from "@/lib/source";

export default function PublicDocsLayout({ children }: { children: ReactNode }) {
  return (
    <DocsLayout
      {...docsLayoutOptions()}
      tree={source.getPageTree()}
      tabMode="top"
      sidebar={{
        defaultOpenLevel: 1,
        prefetch: false,
      }}
      containerProps={{
        className: "bg-background",
      }}
    >
      {children}
    </DocsLayout>
  );
}
