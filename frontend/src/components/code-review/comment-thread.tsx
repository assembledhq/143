"use client";

import { memo, useState } from "react";
import { Check, Edit2, Trash2, Undo2, MessageSquare } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type { SessionReviewComment } from "@/lib/types";
import { CommentInput } from "./comment-input";

/**
 * Lightweight inline markdown renderer for review comments.
 * Supports: **bold**, `code`, _italic_, and preserves line breaks.
 * Escapes HTML to prevent XSS. Uses bounded quantifiers to prevent ReDoS.
 */
function renderCommentMarkdown(text: string): string {
  let html = text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
  // `code` spans — bounded to 500 chars max to prevent ReDoS
  html = html.replace(/`([^`]{1,500})`/g, '<code class="bg-muted px-1 py-0.5 rounded text-xs font-mono">$1</code>');
  // **bold** — bounded
  html = html.replace(/\*\*([^*]{1,500})\*\*/g, "<strong>$1</strong>");
  // _italic_ — bounded
  html = html.replace(/\b_([^_]{1,500})_\b/g, "<em>$1</em>");
  // line breaks
  html = html.replace(/\n/g, "<br />");
  return html;
}

interface CommentThreadProps {
  comments: SessionReviewComment[];
  onUpdate: (commentId: string, data: { body?: string; resolved?: boolean }) => void;
  onDelete: (commentId: string) => void;
  className?: string;
  onRequestEdit?: (comment: SessionReviewComment) => void;
}

function formatRelativeTime(dateStr: string): string {
  const now = Date.now();
  const then = new Date(dateStr).getTime();
  const diffMs = now - then;
  const diffSecs = Math.floor(diffMs / 1000);
  if (diffSecs < 60) return "just now";
  const diffMins = Math.floor(diffSecs / 60);
  if (diffMins < 60) return `${diffMins}m ago`;
  const diffHours = Math.floor(diffMins / 60);
  if (diffHours < 24) return `${diffHours}h ago`;
  const diffDays = Math.floor(diffHours / 24);
  return `${diffDays}d ago`;
}

const SingleComment = memo(function SingleComment({
  comment,
  onUpdate,
  onDelete,
  onRequestEdit,
}: {
  comment: SessionReviewComment;
  onUpdate: (commentId: string, data: { body?: string; resolved?: boolean }) => void;
  onDelete: (commentId: string) => void;
  onRequestEdit?: (comment: SessionReviewComment) => void;
}) {
  const [editing, setEditing] = useState(false);

  if (editing && !onRequestEdit) {
    return (
      <CommentInput
        initialValue={comment.body}
        submitLabel="Save"
        className="mx-2"
        onSubmit={(body) => {
          onUpdate(comment.id, { body });
          setEditing(false);
        }}
        onCancel={() => setEditing(false)}
      />
    );
  }

  return (
    <div
      className={cn(
        "border-l-2 px-3 py-2 text-xs",
        comment.resolved
          ? "border-muted-foreground/20 bg-muted/10"
          : "border-primary/40 bg-primary/5"
      )}
    >
      <div className="mb-1 flex flex-wrap items-start gap-x-2 gap-y-1">
        <div className="flex min-w-0 flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
          <MessageSquare className="h-3 w-3" />
          <span className="font-medium text-foreground/80">You</span>
          <span>{formatRelativeTime(comment.created_at)}</span>
          {comment.pass_number > 0 && (
            <span className="inline-flex items-center rounded-full px-1.5 py-0.5 bg-muted text-muted-foreground text-xs font-medium">
              Pass {comment.pass_number}
            </span>
          )}
          {comment.resolved && (
            <span className="text-emerald-600 dark:text-emerald-400 flex items-center gap-0.5">
              <Check className="h-3 w-3" />
              {comment.resolved_by_pass ? `Resolved in pass ${comment.resolved_by_pass}` : "Resolved"}
            </span>
          )}
        </div>
        <div className="ml-auto flex shrink-0 items-center gap-0.5">
          {comment.resolved ? (
            <Button
              variant="ghost"
              size="sm"
              className="h-6 w-6 p-0 text-muted-foreground hover:text-foreground"
              title="Unresolve"
              onClick={() => onUpdate(comment.id, { resolved: false })}
            >
              <Undo2 className="h-3 w-3" />
            </Button>
          ) : (
            <Button
              variant="ghost"
              size="sm"
              className="h-6 w-6 p-0 text-muted-foreground hover:text-emerald-600"
              title="Resolve"
              onClick={() => onUpdate(comment.id, { resolved: true })}
            >
              <Check className="h-3 w-3" />
            </Button>
          )}
          <Button
            variant="ghost"
            size="sm"
            className="h-6 w-6 p-0 text-muted-foreground hover:text-foreground"
            title="Edit"
            onClick={() => {
              if (onRequestEdit) {
                onRequestEdit(comment);
                return;
              }
              setEditing(true);
            }}
          >
            <Edit2 className="h-3 w-3" />
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="h-6 w-6 p-0 text-muted-foreground hover:text-destructive"
            title="Delete"
            onClick={() => onDelete(comment.id)}
          >
            <Trash2 className="h-3 w-3" />
          </Button>
        </div>
      </div>
      <div
        className={cn("whitespace-pre-wrap", comment.resolved && "text-muted-foreground")}
        // Safe: renderCommentMarkdown escapes HTML before adding formatting tags
        dangerouslySetInnerHTML={{ __html: renderCommentMarkdown(comment.body) }}
      />
    </div>
  );
});

/**
 * Displays a stack of comments for a single line, with collapsed resolved view.
 */
export function CommentThread({ comments, onUpdate, onDelete, className, onRequestEdit }: CommentThreadProps) {
  const [showResolved, setShowResolved] = useState(false);

  const openComments = comments.filter((c) => !c.resolved);
  const resolvedComments = comments.filter((c) => c.resolved);

  return (
    <div
      data-testid="comment-thread"
      className={cn(
        "mx-2 my-1 w-fit max-w-full space-y-0.5",
        className
      )}
    >
      {/* Open comments always shown */}
      {openComments.map((c) => (
        <SingleComment key={c.id} comment={c} onUpdate={onUpdate} onDelete={onDelete} onRequestEdit={onRequestEdit} />
      ))}

      {/* Resolved comments collapsed by default */}
      {resolvedComments.length > 0 && (
        <>
          {showResolved ? (
            <>
              {resolvedComments.map((c) => (
                <SingleComment key={c.id} comment={c} onUpdate={onUpdate} onDelete={onDelete} onRequestEdit={onRequestEdit} />
              ))}
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setShowResolved(false)}
                className="h-auto text-xs text-muted-foreground/60 hover:text-muted-foreground px-3 py-0.5"
              >
                Hide resolved
              </Button>
            </>
          ) : (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setShowResolved(true)}
              className="flex items-center gap-1 h-auto text-xs text-muted-foreground/60 hover:text-muted-foreground px-3 py-1"
            >
              <Check className="h-3 w-3" />
              {resolvedComments.length} resolved comment{resolvedComments.length > 1 ? "s" : ""}
            </Button>
          )}
        </>
      )}
    </div>
  );
}
