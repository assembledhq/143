"use client";

import { memo, useCallback, useMemo, useState } from "react";
import { ChevronDown, ChevronRight, Search, FileText } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type { DiffFile } from "@/lib/diff-parser";

interface FileTreeProps {
  files: DiffFile[];
  activeFileIndex: number;
  onFileSelect: (index: number) => void;
  variant?: "sidebar" | "sheet";
}

interface TreeNode {
  name: string;
  fullPath: string;
  children: Map<string, TreeNode>;
  fileIndex?: number;
  file?: DiffFile;
}

const INITIAL_VISIBLE_FILE_COUNT = 25;
const VISIBLE_FILE_INCREMENT = 250;

function flattenLeafNodes(node: TreeNode): TreeNode[] {
  const leaves: TreeNode[] = [];

  for (const child of node.children.values()) {
    if (child.fileIndex === undefined) {
      leaves.push(...flattenLeafNodes(child));
      continue;
    }
    leaves.push(child);
  }

  return leaves;
}

function treePreservesIncomingOrder(tree: TreeNode, files: DiffFile[]): boolean {
  const leaves = flattenLeafNodes(tree);
  if (leaves.length !== files.length) return false;
  return leaves.every((leaf, index) => leaf.file === files[index]);
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

function FileRow({
  label,
  fileIndex,
  file,
  activeFileIndex,
  onFileSelect,
  depth = 0,
}: {
  label: string;
  fileIndex: number;
  file: DiffFile;
  activeFileIndex: number;
  onFileSelect: (index: number) => void;
  depth?: number;
}) {
  return (
    <button
      onClick={() => onFileSelect(fileIndex)}
      className={cn(
        "flex items-center gap-1.5 w-full px-2 py-1 text-xs rounded transition-colors text-left",
        fileIndex === activeFileIndex
          ? "bg-primary/10 text-primary font-medium"
          : "text-foreground hover:bg-muted/50"
      )}
      style={{ paddingLeft: `${depth * 12 + 8}px` }}
    >
      <FileText className="h-3 w-3 shrink-0 text-muted-foreground" />
      <span className="truncate flex-1">{label}</span>
      <span className="shrink-0 text-xs font-mono text-muted-foreground">
        <span className="text-green-600 dark:text-green-400">+{file.stats.added}</span>
        {" "}
        <span className="text-red-600 dark:text-red-400">-{file.stats.removed}</span>
      </span>
    </button>
  );
}

const TreeDirectory = memo(function TreeDirectory({
  node,
  activeFileIndex,
  onFileSelect,
  depth = 0,
}: {
  node: TreeNode;
  activeFileIndex: number;
  onFileSelect: (index: number) => void;
  depth?: number;
}) {
  const [expanded, setExpanded] = useState(true);
  const entries = [...node.children.values()];

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
          {entries.map((entry) => {
            if (entry.fileIndex === undefined) {
              return (
                <TreeDirectory
                  key={entry.fullPath}
                  node={entry}
                  activeFileIndex={activeFileIndex}
                  onFileSelect={onFileSelect}
                  depth={node.name ? depth + 1 : depth}
                />
              );
            }

            return (
              <FileRow
                key={entry.fullPath}
                label={entry.name}
                fileIndex={entry.fileIndex}
                file={entry.file!}
                activeFileIndex={activeFileIndex}
                onFileSelect={onFileSelect}
                depth={(node.name ? depth + 1 : depth)}
              />
            );
          })}
        </>
      )}
    </div>
  );
});

export const FileTree = memo(function FileTree({
  files,
  activeFileIndex,
  onFileSelect,
  variant = "sidebar",
}: FileTreeProps) {
  const [filter, setFilter] = useState("");
  const [visibleState, setVisibleState] = useState({
    files,
    filter,
    count: INITIAL_VISIBLE_FILE_COUNT,
  });
  let visibleCount = visibleState.count;
  if (visibleState.files !== files || visibleState.filter !== filter) {
    visibleCount = INITIAL_VISIBLE_FILE_COUNT;
    setVisibleState({ files, filter, count: INITIAL_VISIBLE_FILE_COUNT });
  }

  const filteredFiles = useMemo(() => {
    if (!filter.trim()) return files;
    const q = filter.toLowerCase();
    return files.filter((f) => f.newPath.toLowerCase().includes(q));
  }, [files, filter]);
  const visibleFiles = useMemo(
    () => filteredFiles.slice(0, visibleCount),
    [filteredFiles, visibleCount]
  );
  const hasMoreFiles = visibleFiles.length < filteredFiles.length;

  // Build a reference-identity map from DiffFile -> original index for O(1) lookups
  const fileToOrigIndex = useMemo(() => {
    const map = new Map<DiffFile, number>();
    files.forEach((file, idx) => map.set(file, idx));
    return map;
  }, [files]);

  // Map original indices to visible indices for active file tracking.
  const visibleIndexMap = useMemo(() => {
    const map = new Map<number, number>();
    visibleFiles.forEach((file, visibleIdx) => {
      const origIdx = fileToOrigIndex.get(file) ?? -1;
      if (origIdx >= 0) map.set(origIdx, visibleIdx);
    });
    return map;
  }, [fileToOrigIndex, visibleFiles]);

  const tree = useMemo(
    () => flattenSingleChildDirs(buildTree(visibleFiles)),
    [visibleFiles]
  );
  const useFlatFileOrder = useMemo(
    () => !treePreservesIncomingOrder(tree, visibleFiles),
    [tree, visibleFiles]
  );

  // Convert activeFileIndex from original files array to the visible position.
  const visibleActiveIndex = visibleIndexMap.get(activeFileIndex) ?? activeFileIndex;

  // When a file is selected in the visible tree, convert back to original index.
  const handleFileSelect = useCallback((visibleIdx: number) => {
    const file = visibleFiles[visibleIdx];
    if (!file) return;
    const origIdx = fileToOrigIndex.get(file) ?? -1;
    if (origIdx >= 0) onFileSelect(origIdx);
  }, [fileToOrigIndex, visibleFiles, onFileSelect]);

  return (
    <div className="flex flex-col h-full">
      <div className={cn("px-4 pb-3 pt-3", variant === "sheet" && "pt-1")}>
        <p className="text-xs font-medium text-muted-foreground/70 uppercase tracking-wider mb-2">
          {files.length} files changed
        </p>
        <div className="relative">
          <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground/50" />
          <Input
            placeholder="Filter files..."
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            className="h-8 border-border/70 bg-background pl-7 pr-2 text-xs placeholder:text-muted-foreground/50"
          />
        </div>
        {variant === "sheet" ? (
          <p className="mt-2 text-xs text-muted-foreground/70">
            Select a file to jump back into the full-screen diff reader.
          </p>
        ) : null}
      </div>
      <div className="flex-1 overflow-y-auto scrollbar-hide px-3 pb-2">
        {useFlatFileOrder ? (
          visibleFiles.map((file, visibleIdx) => (
            <FileRow
              key={`${file.newPath}:${visibleIdx}`}
              label={file.newPath}
              fileIndex={visibleIdx}
              file={file}
              activeFileIndex={visibleActiveIndex}
              onFileSelect={handleFileSelect}
            />
          ))
        ) : (
          <TreeDirectory
            node={tree}
            activeFileIndex={visibleActiveIndex}
            onFileSelect={handleFileSelect}
          />
        )}
        {hasMoreFiles ? (
          <div className="sticky bottom-0 bg-background/95 py-3 backdrop-blur">
            <p className="mb-2 text-center text-xs text-muted-foreground">
              Showing {visibleFiles.length} of {filteredFiles.length} files
            </p>
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="w-full text-xs"
              onClick={() => setVisibleState((state) => ({
                files,
                filter,
                count: state.count + VISIBLE_FILE_INCREMENT,
              }))}
            >
              Show more files
            </Button>
          </div>
        ) : null}
      </div>
    </div>
  );
});

FileTree.displayName = "FileTree";
