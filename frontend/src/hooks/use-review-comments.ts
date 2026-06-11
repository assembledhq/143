import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useMemo, useCallback, useRef } from "react";
import { api } from "@/lib/api";
import type { ListResponse, SessionReviewComment } from "@/lib/types";

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
  const pendingIDCounter = useRef(0);
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
    onMutate: async (body) => {
      await queryClient.cancelQueries({ queryKey });
      pendingIDCounter.current += 1;
      const optimisticID = `pending-${Date.now()}-${pendingIDCounter.current}`;
      const now = new Date().toISOString();
      const optimisticComment: SessionReviewComment = {
        id: optimisticID,
        session_id: sessionId,
        org_id: "",
        user_id: "",
        file_path: body.file_path,
        line_number: body.line_number,
        diff_side: body.side ?? "new",
        body: body.body,
        resolved: false,
        pass_number: 1,
        created_at: now,
        updated_at: now,
      };

      queryClient.setQueryData<ListResponse<SessionReviewComment>>(queryKey, (previous) => ({
        data: [...(previous?.data ?? []), optimisticComment],
        meta: previous?.meta ?? {},
      }));

      return { optimisticID };
    },
    onSuccess: (response, _variables, context) => {
      queryClient.setQueryData<ListResponse<SessionReviewComment>>(queryKey, (previous) => {
        if (!previous) {
          return { data: [response.data], meta: {} };
        }
        return {
          ...previous,
          data: previous.data.map((comment) =>
            comment.id === context?.optimisticID ? response.data : comment
          ),
        };
      });
    },
    onError: (_error, _variables, context) => {
      queryClient.setQueryData<ListResponse<SessionReviewComment>>(queryKey, (previous) => {
        if (!previous) return previous;
        return {
          ...previous,
          data: previous.data.filter((comment) => comment.id !== context?.optimisticID),
        };
      });
    },
    onSettled: invalidate,
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
