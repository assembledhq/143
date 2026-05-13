import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useMemo, useCallback } from "react";
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

  const invalidate = useCallback(() => {
    queryClient.invalidateQueries({ queryKey });
  }, [queryClient, queryKey]);

  const createMutation = useMutation({
    mutationFn: (body: {
      file_path: string;
      line_number: number;
      side?: DiffSide;
      body: string;
    }) => api.sessions.createReviewComment(sessionId, body),
    onSuccess: invalidate,
  });
  const { mutate: createReviewComment, isPending: isCreating } = createMutation;

  const updateMutation = useMutation({
    mutationFn: ({
      commentId,
      data,
    }: {
      commentId: string;
      data: { body?: string; resolved?: boolean };
    }) => api.sessions.updateReviewComment(sessionId, commentId, data),
    onSuccess: invalidate,
  });
  const { mutate: updateReviewComment } = updateMutation;

  const deleteMutation = useMutation({
    mutationFn: (commentId: string) =>
      api.sessions.deleteReviewComment(sessionId, commentId),
    onSuccess: invalidate,
  });
  const { mutate: deleteReviewComment } = deleteMutation;

  const createComment = useCallback(
    (data: {
      file_path: string;
      line_number: number;
      side?: DiffSide;
      body: string;
    }) => {
      createReviewComment(data);
    },
    [createReviewComment]
  );

  const updateComment = useCallback(
    (commentId: string, data: { body?: string; resolved?: boolean }) => {
      updateReviewComment({ commentId, data });
    },
    [updateReviewComment]
  );

  const deleteComment = useCallback(
    (commentId: string) => {
      deleteReviewComment(commentId);
    },
    [deleteReviewComment]
  );

  // Derive error from per-mutation state so concurrent mutations don't clear each other's errors.
  const mutationError = createMutation.error ?? updateMutation.error ?? deleteMutation.error;
  const errorMessage = mutationError
    ? mutationError instanceof Error
      ? mutationError.message
      : "An error occurred"
    : null;

  return {
    comments,
    commentsByLine,
    openCount,
    resolvedCount,
    isLoading,
    error: errorMessage,
    createComment,
    updateComment,
    deleteComment,
    isCreating,
  };
}
