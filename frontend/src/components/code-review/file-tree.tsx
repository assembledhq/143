"use client";

import { memo, useMemo, useState } from "react";
import { ChevronDown, ChevronRight, Search, FileText, Check } from "lucide-react";
import { cn } from "@/lib/utils";
import type { DiffFile } from "@/lib/diff-parser";

interface FileTreeProps {
  files: DiffFile[];
  activeFileIndex: number;
  onFileSelect: (index: number) => void;
  reviewedFiles?: Set<string>;
  onToggleReviewed?: (filePath: string) => void;
}

interface TreeNode {
  name: string;
  fullPath: string;
  children: Map<string, TreeNode>;
  fileIndex?: number;
  file?: DiffFile;
}

function buildTree(files: DiffFile[]): TreeNode {
  const root: TreeNode = { name: "", fullPath: "", children: new Map() };

  files.forEach((file, index) => {
    const parts = file.newPath.split("/");
    let current = root;

    for (let i = 0; i < parts.length; i++) {
      const part = parts[i];
      if (!current.children.has(part)) {
        current.children.set(part, {
          name: part,
          fullPath: parts.slice(0, i + 1).join("/"),
          children: new Map(),
        });
      }
      current = current.children.get(part)!;
    }

    current.fileIndex = index;
    current.file = file;
  });

  return root;
}

function flattenSingleChildDirs(node: TreeNode): TreeNode {
  // If a directory has only one child and that child is also a directory, collapse them
  if (node.children.size === 1 && node.fileIndex === undefined) {
    const [, child] = [...node.children.entries()][0];
    if (child.children.size > 0 && child.fileIndex === undefined) {
      const collapsed = flattenSingleChildDirs(child);
      return {
        ...collapsed,
        name: node.name ? `${node.name}/${collapsed.name}` : collapsed.name,
      };
    }
  }

  const newChildren = new Map<string, TreeNode>();
  for (const [key, child] of node.children) {
    const flattened = flattenSingleChildDirs(child);
    newChildren.set(key, flattened);
  }

  return { ...node, children: newChildren };
}

/**
 * Sort files by impact: largest changes first, then alphabetical.
 */
function sortFilesByImpact(files: DiffFile[]): DiffFile[] {
  return [...files].sort((a, b) => {
    const aImpact = a.stats.added + a.stats.removed;
    const bImpact = b.stats.added + b.stats.removed;
    if (bImpact !== aImpact) return bImpact - aImpact;
    return a.newPath.localeCompare(b.newPath);
  });
}

const TreeDirectory = memo(function TreeDirectory({
  node,
  activeFileIndex,
  onFileSelect,
  reviewedFiles,
  onToggleReviewed,
  depth = 0,
}: {
  node: TreeNode;
  activeFileIndex: number;
  onFileSelect: (index: number) => void;
  reviewedFiles?: Set<string>;
  onToggleReviewed?: (filePath: string) => void;
  depth?: number;
}) {
  const [expanded, setExpanded] = useState(true);
  const entries = [...node.children.values()];
  const dirs = entries.filter((n) => n.fileIndex === undefined);
  const files = entries.filter((n) => n.fileIndex !== undefined);

  return (
    <div>
      {node.name && (
        <button
          onClick={() => setExpanded(!expanded)}
          aria-expanded={expanded}
          className="flex items-center gap-1 w-full px-2 py-1 text-xs font-medium text-muted-foreground hover:text-foreground hover:bg-muted/50 rounded transition-colors"
          style={{ paddingLeft: `${depth * 12 + 8}px` }}
        >
          {expanded ? (
            <ChevronDown className="h-3 w-3 shrink-0" />
          ) : (
            <ChevronRight className="h-3 w-3 shrink-0" />
          )}
          <span className="truncate">{node.name}/</span>
        </button>
      )}
      {expanded && (
        <>
          {dirs.map((dir) => (
            <TreeDirectory
              key={dir.fullPath}
              node={dir}
              activeFileIndex={activeFileIndex}
              onFileSelect={onFileSelect}
              reviewedFiles={reviewedFiles}
              onToggleReviewed={onToggleReviewed}
              depth={node.name ? depth + 1 : depth}
            />
          ))}
          {files.map((fileNode) => {
            const isReviewed = reviewedFiles?.has(fileNode.file!.newPath);
            return (
              <div
                key={fileNode.fullPath}
                className={cn(
                  "flex items-center gap-1.5 w-full px-2 py-1 text-xs rounded transition-colors",
                  fileNode.fileIndex === activeFileIndex
                    ? "bg-primary/10 text-primary font-medium"
                    : "text-foreground hover:bg-muted/50"
                )}
                style={{ paddingLeft: `${(node.name ? depth + 1 : depth) * 12 + 8}px` }}
              >
                {/* Review checkmark */}
                {onToggleReviewed ? (
                  <button
                    onClick={(e) => {
                      e.stopPropagation();
                      onToggleReviewed(fileNode.file!.newPath);
                    }}
                    className={cn(
                      "h-3.5 w-3.5 shrink-0 rounded-sm border flex items-center justify-center transition-colors",
                      isReviewed
                        ? "bg-emerald-500/20 border-emerald-500/40 text-emerald-600 dark:text-emerald-400"
                        : "border-border text-transparent hover:border-muted-foreground/40"
                    )}
                    title={isReviewed ? "Mark as not reviewed" : "Mark as reviewed"}
                    aria-label={`${isReviewed ? "Unmark" : "Mark"} ${fileNode.name} as reviewed`}
                    role="checkbox"
                    aria-checked={isReviewed}
                  >
                    <Check className="h-2.5 w-2.5" />
                  </button>
                ) : (
                  <FileText className="h-3 w-3 shrink-0 text-muted-foreground" />
                )}
                <button
                  onClick={() => onFileSelect(fileNode.fileIndex!)}
                  className="truncate flex-1 text-left"
                >
                  {fileNode.name}
                </button>
                {fileNode.file && (
                  <span className="shrink-0 text-xs font-mono text-muted-foreground">
                    <span className="text-green-600 dark:text-green-400">+{fileNode.file.stats.added}</span>
                    {" "}
                    <span className="text-red-600 dark:text-red-400">-{fileNode.file.stats.removed}</span>
                  </span>
                )}
              </div>
            );
          })}
        </>
      )}
    </div>
  );
});

export function FileTree({
  files,
  activeFileIndex,
  onFileSelect,
  reviewedFiles,
  onToggleReviewed,
}: FileTreeProps) {
  const [filter, setFilter] = useState("");

  // Sort files by impact (largest changes first) then filter
  const sortedFiles = useMemo(() => sortFilesByImpact(files), [files]);

  const filteredFiles = useMemo(() => {
    if (!filter.trim()) return sortedFiles;
    const q = filter.toLowerCase();
    return sortedFiles.filter((f) => f.newPath.toLowerCase().includes(q));
  }, [sortedFiles, filter]);

  // Build a reference-identity map from DiffFile -> original index for O(1) lookups
  const fileToOrigIndex = useMemo(() => {
    const map = new Map<DiffFile, number>();
    files.forEach((file, idx) => map.set(file, idx));
    return map;
  }, [files]);

  // Map original indices to sorted indices for active file tracking
  const sortedIndexMap = useMemo(() => {
    const map = new Map<number, number>();
    sortedFiles.forEach((file, sortedIdx) => {
      const origIdx = fileToOrigIndex.get(file) ?? -1;
      if (origIdx >= 0) map.set(origIdx, sortedIdx);
    });
    return map;
  }, [fileToOrigIndex, sortedFiles]);

  const tree = useMemo(
    () => flattenSingleChildDirs(buildTree(filteredFiles)),
    [filteredFiles]
  );

  // Convert activeFileIndex from original files array to the sorted position
  const sortedActiveIndex = sortedIndexMap.get(activeFileIndex) ?? activeFileIndex;

  // When a file is selected in the sorted tree, convert back to original index
  const handleFileSelect = (sortedIdx: number) => {
    const file = filteredFiles[sortedIdx];
    if (!file) return;
    const origIdx = fileToOrigIndex.get(file) ?? -1;
    if (origIdx >= 0) onFileSelect(origIdx);
  };

  return (
    <div className="flex flex-col h-full">
      <div className="px-4 pb-3">
        <p className="text-xs font-medium text-muted-foreground/70 uppercase tracking-wider mb-2">
          {files.length} files changed
        </p>
        <div className="relative">
          <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground/50" />
          <input
            type="text"
            placeholder="Filter files..."
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            className="w-full h-7 pl-7 pr-2 rounded-md border border-border bg-background text-xs placeholder:text-muted-foreground/50 focus:outline-none focus:ring-1 focus:ring-ring"
          />
        </div>
      </div>
      <div className="flex-1 overflow-y-auto px-3 pb-2">
        <TreeDirectory
          node={tree}
          activeFileIndex={sortedActiveIndex}
          onFileSelect={handleFileSelect}
          reviewedFiles={reviewedFiles}
          onToggleReviewed={onToggleReviewed}
        />
      </div>
    </div>
  );
}
