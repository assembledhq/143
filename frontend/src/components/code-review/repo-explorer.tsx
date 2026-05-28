"use client";

import { useCallback, useMemo, useState } from "react";
import {
  FileText,
  Folder,
  FolderOpen,
  ArrowLeft,
  Loader2,
  AlertCircle,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { useSessionFileList, useSessionFileContent } from "@/hooks/use-session-files";
import { useFileHighlighting } from "@/lib/syntax-highlighter";
import { inferLanguage } from "@/lib/diff-parser";
import type { FileEntry } from "@/lib/types";
import type { DiffFile } from "@/lib/diff-parser";

// ---------------------------------------------------------------------------
// Breadcrumb
// ---------------------------------------------------------------------------

function FileBreadcrumb({
  path,
  onNavigate,
}: {
  path: string;
  onNavigate: (path: string) => void;
}) {
  const parts = path ? path.split("/") : [];

  return (
    <div className="flex items-center gap-0.5 text-xs text-muted-foreground overflow-x-auto min-w-0">
      <button
        onClick={() => onNavigate("")}
        className="shrink-0 hover:text-foreground transition-colors px-1 py-0.5 rounded hover:bg-surface-hover"
      >
        root
      </button>
      {parts.map((part, i) => {
        const partPath = parts.slice(0, i + 1).join("/");
        const isLast = i === parts.length - 1;
        return (
          <span key={partPath} className="flex items-center gap-0.5">
            <span className="text-muted-foreground/40">/</span>
            {isLast ? (
              <span className="text-foreground font-medium px-1 py-0.5">
                {part}
              </span>
            ) : (
              <button
                onClick={() => onNavigate(partPath)}
                className="hover:text-foreground transition-colors px-1 py-0.5 rounded hover:bg-surface-hover"
              >
                {part}
              </button>
            )}
          </span>
        );
      })}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Directory tree (left panel)
// ---------------------------------------------------------------------------

function DirectoryTreeEntry({
  entry,
  isActive,
  onSelect,
  changedFiles,
}: {
  entry: FileEntry;
  isActive: boolean;
  onSelect: (entry: FileEntry) => void;
  changedFiles: Set<string>;
}) {
  const isChanged = changedFiles.has(entry.path);
  const Icon =
    entry.type === "dir" ? Folder : FileText;

  return (
    <button
      onClick={() => onSelect(entry)}
      className={cn(
        "flex items-center gap-1.5 w-full px-2 py-1 text-xs rounded transition-colors text-left",
        isActive
          ? "bg-primary/10 text-primary font-medium"
          : "text-foreground hover:bg-surface-hover"
      )}
    >
      <Icon className="h-3 w-3 shrink-0 text-muted-foreground" />
      <span className="truncate flex-1">{entry.path.split("/").pop()}</span>
      {isChanged && (
        <span className="h-1.5 w-1.5 rounded-full bg-amber-500 shrink-0" title="Modified in diff" />
      )}
    </button>
  );
}

function DirectoryTree({
  sessionId,
  currentPath,
  onNavigate,
  activePath,
  changedFiles,
}: {
  sessionId: string;
  currentPath: string;
  onNavigate: (path: string, type?: "file" | "dir") => void;
  activePath: string;
  changedFiles: Set<string>;
}) {
  const { data, isLoading, error } = useSessionFileList(sessionId, currentPath);

  const entries = useMemo(() => data?.data ?? [], [data?.data]);

  // Separate dirs and files, sort alphabetically
  const sortedEntries = useMemo(() => {
    const dirs = entries.filter((e) => e.type === "dir").sort((a, b) => a.path.localeCompare(b.path));
    const files = entries.filter((e) => e.type === "file").sort((a, b) => a.path.localeCompare(b.path));
    return [...dirs, ...files];
  }, [entries]);

  const handleSelect = useCallback(
    (entry: FileEntry) => {
      onNavigate(entry.path, entry.type);
    },
    [onNavigate]
  );

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-4">
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="flex items-center gap-1.5 px-2 py-3 text-xs text-muted-foreground">
        <AlertCircle className="h-3 w-3 shrink-0" />
        <span>Cannot read directory</span>
      </div>
    );
  }

  if (sortedEntries.length === 0) {
    return (
      <div className="px-2 py-3 text-xs text-muted-foreground">
        Empty directory
      </div>
    );
  }

  return (
    <div className="space-y-0.5">
      {sortedEntries.map((entry) => (
        <DirectoryTreeEntry
          key={entry.path}
          entry={entry}
          isActive={entry.path === activePath}
          onSelect={handleSelect}
          changedFiles={changedFiles}
        />
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// File viewer (right panel)
// ---------------------------------------------------------------------------

function FileViewer({
  sessionId,
  filePath,
  changedLineNumbers,
}: {
  sessionId: string;
  filePath: string;
  changedLineNumbers: Set<number>;
}) {
  const { data, isLoading, error } = useSessionFileContent(sessionId, filePath);

  const fileContent = data?.data;
  const content = fileContent?.content;
  const lines = useMemo(
    () => (content ? content.split("\n") : []),
    [content]
  );

  const lang = fileContent?.language || inferLanguage(filePath);
  const highlighted = useFileHighlighting(lines, lang);

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (error || !fileContent) {
    return (
      <div className="flex flex-col items-center justify-center py-12 gap-2 text-sm text-muted-foreground">
        <AlertCircle className="h-5 w-5" />
        <p>Unable to load file content</p>
      </div>
    );
  }

  return (
    <div className="overflow-auto font-mono text-xs leading-[18px]">
      <table className="w-full border-collapse">
        <tbody>
          {lines.map((line, i) => {
            const lineNum = i + 1;
            const isChanged = changedLineNumbers.has(lineNum);
            return (
              <tr
                key={lineNum}
                className={cn(
                  "hover:bg-surface-pane",
                  isChanged && "bg-amber-500/5"
                )}
              >
                {/* Changed indicator gutter */}
                <td className="w-[3px] p-0">
                  {isChanged && (
                    <div className="w-[3px] h-full bg-amber-500/60" />
                  )}
                </td>
                {/* Line number */}
                <td className="w-[50px] px-2 text-right text-xs text-muted-foreground/50 select-none align-top whitespace-nowrap">
                  {lineNum}
                </td>
                {/* Content */}
                <td className="px-3 whitespace-pre">
                  {highlighted ? (
                    <span
                      dangerouslySetInnerHTML={{
                        __html: highlighted[i] ?? "",
                      }}
                    />
                  ) : (
                    line
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Main RepoExplorer component
// ---------------------------------------------------------------------------

interface RepoExplorerProps {
  sessionId: string;
  /** Files from the diff, used to mark changed files and lines */
  diffFiles: DiffFile[];
  onBack: () => void;
  /** Optional file path to open initially (e.g. from clicking a file in the diff) */
  initialPath?: string;
}

export function RepoExplorer({
  sessionId,
  diffFiles,
  onBack,
  initialPath,
}: RepoExplorerProps) {
  const [currentDir, setCurrentDir] = useState(() => {
    if (initialPath) {
      // If initial path is a file, navigate to its parent dir
      const parts = initialPath.split("/");
      if (parts.length > 1) {
        return parts.slice(0, -1).join("/");
      }
    }
    return "";
  });
  const [selectedFile, setSelectedFile] = useState<string>(initialPath || "");

  // Build a set of changed file paths from the diff
  const changedFiles = useMemo(() => {
    const set = new Set<string>();
    for (const f of diffFiles) {
      set.add(f.newPath);
    }
    return set;
  }, [diffFiles]);

  // Build a map of changed line numbers per file from the diff
  const changedLinesByFile = useMemo(() => {
    const map = new Map<string, Set<number>>();
    for (const f of diffFiles) {
      const lineNums = new Set<number>();
      for (const hunk of f.hunks) {
        for (const line of hunk.lines) {
          if (line.type === "add" && line.newLineNumber != null) {
            lineNums.add(line.newLineNumber);
          }
        }
      }
      if (lineNums.size > 0) {
        map.set(f.newPath, lineNums);
      }
    }
    return map;
  }, [diffFiles]);

  const handleNavigate = useCallback(
    (path: string, type?: "file" | "dir") => {
      if (type === "file") {
        setSelectedFile(path);
        const parts = path.split("/");
        if (parts.length > 1) {
          setCurrentDir(parts.slice(0, -1).join("/"));
        }
      } else if (type === "dir") {
        setCurrentDir(path);
        setSelectedFile("");
      } else {
        // Fallback for breadcrumb navigation where we don't have type info.
        // Breadcrumbs always navigate to directories (intermediate path segments).
        setCurrentDir(path);
        setSelectedFile("");
      }
    },
    []
  );

  const handleGoUp = useCallback(() => {
    if (!currentDir) return;
    const parts = currentDir.split("/");
    if (parts.length <= 1) {
      setCurrentDir("");
    } else {
      setCurrentDir(parts.slice(0, -1).join("/"));
    }
    setSelectedFile("");
  }, [currentDir]);

  const changedLinesForSelected = selectedFile
    ? changedLinesByFile.get(selectedFile) ?? new Set<number>()
    : new Set<number>();

  return (
    <div className="flex flex-col h-full">
      {/* Header */}
      <div className="flex items-center gap-2 px-3 py-1.5 border-b border-border bg-surface-raised shrink-0">
        <Button
          variant="ghost"
          size="sm"
          className="h-7 gap-1 text-xs px-2"
          onClick={onBack}
        >
          <ArrowLeft className="h-3 w-3" />
          Back to diff
        </Button>
        <div className="h-4 w-px bg-border" />
        <FileBreadcrumb
          path={selectedFile || currentDir}
          onNavigate={handleNavigate}
        />
      </div>

      {/* Main content */}
      <div className="flex flex-1 min-h-0">
        {/* Directory tree (left) */}
        <div className="w-[200px] shrink-0 border-r border-border overflow-y-auto">
          <div className="p-2">
            {currentDir && (
              <button
                onClick={handleGoUp}
                className="flex items-center gap-1.5 w-full px-2 py-1 text-xs text-muted-foreground hover:text-foreground hover:bg-surface-hover rounded transition-colors mb-1"
              >
                <FolderOpen className="h-3 w-3 shrink-0" />
                <span>..</span>
              </button>
            )}
            <DirectoryTree
              sessionId={sessionId}
              currentPath={currentDir}
              onNavigate={handleNavigate}
              activePath={selectedFile}
              changedFiles={changedFiles}
            />
          </div>
        </div>

        {/* File viewer (right) */}
        <div className="flex-1 min-w-0 overflow-auto">
          {selectedFile ? (
            <FileViewer
              sessionId={sessionId}
              filePath={selectedFile}
              changedLineNumbers={changedLinesForSelected}
            />
          ) : (
            <div className="flex items-center justify-center h-full text-sm text-muted-foreground">
              Select a file to view its content
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
