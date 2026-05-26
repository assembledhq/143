import type { ReactNode } from "react";

const sectionIndexPaths = new Set([
  "getting-started/index.mdx",
  "guides/index.mdx",
  "self-hosting/index.mdx",
  "reference/index.mdx",
]);

export function sidebarLabelForPageTreeFile(
  filePath: string | undefined,
  currentName: ReactNode
): ReactNode {
  if (!filePath) {
    return currentName;
  }

  const normalizedPath = filePath.replaceAll("\\", "/");
  if (sectionIndexPaths.has(normalizedPath)) {
    return "Overview";
  }

  return currentName;
}
