import { DocsLayout } from "fumadocs-ui/layouts/docs";
import { RootProvider } from "fumadocs-ui/provider/next";
import type { ReactNode } from "react";
import { docsLayoutOptions } from "@/lib/docs/layout";
import { source } from "@/lib/source";

export default function PublicDocsLayout({ children }: { children: ReactNode }) {
  return (
    <RootProvider theme={{ enabled: false }} search={{ options: { api: "/api/search" } }}>
      <DocsLayout
        {...docsLayoutOptions()}
        tree={source.getPageTree()}
        tabs={false}
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
    </RootProvider>
  );
}
