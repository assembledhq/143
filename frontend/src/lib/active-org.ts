// Per-tab active org selection. Stored in sessionStorage so each browser tab
// can independently focus a different org without stomping its siblings; the
// backend's X-Active-Org-ID header cascade makes this cheap (no server-side
// session churn). The key name is load-bearing — the API request interceptor
// in ./api.ts reads the same key.

const ACTIVE_ORG_KEY = 'active_org_id';

export const ACTIVE_ORG_CHANGED_EVENT = 'active-org-changed';
export const ORG_MEMBERSHIP_REVOKED_EVENT = 'org-membership-revoked';

export function getActiveOrgId(): string | null {
  if (typeof window === 'undefined') return null;
  try {
    return window.sessionStorage.getItem(ACTIVE_ORG_KEY);
  } catch {
    return null;
  }
}

export function setActiveOrgId(id: string | null): void {
  if (typeof window === 'undefined') return;
  try {
    if (id) {
      window.sessionStorage.setItem(ACTIVE_ORG_KEY, id);
    } else {
      window.sessionStorage.removeItem(ACTIVE_ORG_KEY);
    }
  } catch {
    return;
  }
  window.dispatchEvent(new CustomEvent(ACTIVE_ORG_CHANGED_EVENT, { detail: { id } }));
}
