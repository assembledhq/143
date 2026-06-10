import type { SessionDetail } from "@/lib/types";

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
