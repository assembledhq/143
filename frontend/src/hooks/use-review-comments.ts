import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useMemo, useCallback, useState } from "react";
import { api } from "@/lib/api";
import type { SessionReviewComment } from "@/lib/types";

/** Key for grouping comments by file + line + side */
export type DiffSide = "old" | "new";
export type CommentLineKey = `${string}:${number}:${DiffSide}`;

export function makeCommentLineKey(
  filePath: string,
  lineNumber: number,
  side: DiffSide
): CommentLineKey {
  return `${filePath}:${lineNumber}:${side}`;
}

export interface UseReviewCommentsResult {
  comments: SessionReviewComment[];
  commentsByLine: Map<CommentLineKey, SessionReviewComment[]>;
  openCount: number;
  resolvedCount: number;
  isLoading: boolean;
  error: string | null;
  createComment: (data: {
    file_path: string;
    line_number: number;
    side?: DiffSide;
    body: string;
  }) => void;
  updateComment: (
    commentId: string,
    data: { body?: string; resolved?: boolean }
  ) => void;
  deleteComment: (commentId: string) => void;
  isCreating: boolean;
}

export function useReviewComments(sessionId: string): UseReviewCommentsResult {
  const queryClient = useQueryClient();
  const queryKey = useMemo(
    () => ["session", sessionId, "review-comments"],
    [sessionId]
  );

  const { data, isLoading } = useQuery({
    queryKey,
    queryFn: () => api.sessions.listReviewComments(sessionId),
  });

  const comments = useMemo(() => data?.data ?? [], [data?.data]);

  const commentsByLine = useMemo(() => {
    const map = new Map<CommentLineKey, SessionReviewComment[]>();
    for (const c of comments) {
      const key = makeCommentLineKey(c.file_path, c.line_number, c.diff_side);
      const arr = map.get(key);
      if (arr) {
        arr.push(c);
      } else {
        map.set(key, [c]);
      }
    }
    return map;
  }, [comments]);

  const openCount = useMemo(
    () => comments.filter((c) => !c.resolved).length,
    [comments]
  );
  const resolvedCount = useMemo(
    () => comments.filter((c) => c.resolved).length,
    [comments]
  );

  const [mutationError, setMutationError] = useState<string | null>(null);

  const invalidate = useCallback(() => {
    setMutationError(null);
    queryClient.invalidateQueries({ queryKey });
  }, [queryClient, queryKey]);

  const onMutationError = useCallback((err: unknown) => {
    const message = err instanceof Error ? err.message : "An error occurred";
    setMutationError(message);
  }, []);

  const createMutation = useMutation({
    mutationFn: (body: {
      file_path: string;
      line_number: number;
      side?: DiffSide;
      body: string;
    }) => api.sessions.createReviewComment(sessionId, body),
    onSuccess: invalidate,
    onError: onMutationError,
  });

  const updateMutation = useMutation({
    mutationFn: ({
      commentId,
      data,
    }: {
      commentId: string;
      data: { body?: string; resolved?: boolean };
    }) => api.sessions.updateReviewComment(sessionId, commentId, data),
    onSuccess: invalidate,
    onError: onMutationError,
  });

  const deleteMutation = useMutation({
    mutationFn: (commentId: string) =>
      api.sessions.deleteReviewComment(sessionId, commentId),
    onSuccess: invalidate,
    onError: onMutationError,
  });

  const createComment = useCallback(
    (data: {
      file_path: string;
      line_number: number;
      side?: DiffSide;
      body: string;
    }) => {
      createMutation.mutate(data);
    },
    [createMutation]
  );

  const updateComment = useCallback(
    (commentId: string, data: { body?: string; resolved?: boolean }) => {
      updateMutation.mutate({ commentId, data });
    },
    [updateMutation]
  );

  const deleteComment = useCallback(
    (commentId: string) => {
      deleteMutation.mutate(commentId);
    },
    [deleteMutation]
  );

  return {
    comments,
    commentsByLine,
    openCount,
    resolvedCount,
    isLoading,
    error: mutationError,
    createComment,
    updateComment,
    deleteComment,
    isCreating: createMutation.isPending,
  };
}
