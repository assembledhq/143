"use client";

import { memo, useEffect, useState } from "react";

import { cn } from "@/lib/utils";

type MarkdownContentProps = {
  content: string;
  className?: string;
};

type MarkdownRenderer = typeof import("@/components/markdown").MarkdownContent;

export const LazyMarkdownContent = memo(function LazyMarkdownContent({
  content,
  className,
}: MarkdownContentProps) {
  const [MarkdownContent, setMarkdownContent] = useState<MarkdownRenderer | null>(null);

  useEffect(() => {
    let cancelled = false;

    void import("@/components/markdown").then((mod) => {
      if (!cancelled) {
        setMarkdownContent(() => mod.MarkdownContent);
      }
    });

    return () => {
      cancelled = true;
    };
  }, []);

  if (MarkdownContent) {
    return (
      <div className={className}>
        <MarkdownContent content={content} />
      </div>
    );
  }

  return (
    <div className={cn("whitespace-pre-wrap break-words", className)}>
      {content}
    </div>
  );
});
