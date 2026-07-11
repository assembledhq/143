import type { Session, SessionDetail, SingleResponse } from "@/lib/types";

const provisionalSessionDetailKey = "__provisional_session_detail";

type ProvisionalSessionDetail = SessionDetail & {
  [provisionalSessionDetailKey]?: true;
};

export function markProvisionalSessionDetail(session: SessionDetail): SessionDetail {
  return {
    ...session,
    [provisionalSessionDetailKey]: true,
  } as ProvisionalSessionDetail;
}

export function isProvisionalSessionDetail(session: SessionDetail | undefined | null): boolean {
  return Boolean((session as ProvisionalSessionDetail | undefined | null)?.[provisionalSessionDetailKey]);
}

export function provisionalSessionDetailFromListItem(session: Session): SingleResponse<SessionDetail> {
  return {
    data: markProvisionalSessionDetail({
      ...session,
      threads: session.threads ?? [],
      changesets: [],
    }),
  };
}
