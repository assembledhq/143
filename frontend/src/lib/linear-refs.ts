// Mirror of the backend detection regexes in
// internal/services/linear/detect.go. Keep in lockstep so the composer
// affordance accepts exactly what ScanInputs would pick up — anything looser
// shows the user a "linked" UI for input the backend will silently ignore;
// anything stricter rejects valid keys that the linker would have caught.
//
// The trailing `(?:[/?#][^\s]*)?$` matches anything Linear's "Copy issue
// link" might suffix (slug paths like `/comment/abc`, query strings like
// `?focused=…`, anchors like `#section`). The backend's URL pattern is
// unanchored and stops capture at the issue key — but the *whole pasted
// string* still needs to be accepted by this UX gate, otherwise users who
// paste a tracker-decorated URL see "Enter a Linear URL…" while the backend
// would happily extract the key.
const linearURLPattern = /^https?:\/\/linear\.app\/[^/\s]+\/issue\/[A-Z][A-Z0-9_]{0,9}-[0-9]+(?:[/?#][^\s]*)?$/;
const linearBareIdentifierPattern = /^[A-Z][A-Z0-9_]{0,9}-[0-9]+$/;

// looksLikeLinearRef returns true when input is a plausibly-linkable Linear
// reference (URL or bare key). Used in the composer Linear-input affordance
// to reject obvious garbage before submit. The backend re-validates with the
// org's team-key allowlist, so this is a UX hint only — never a security
// boundary.
export function looksLikeLinearRef(input: string): boolean {
  const trimmed = input.trim();
  if (trimmed.length === 0) {
    return false;
  }
  return linearURLPattern.test(trimmed) || linearBareIdentifierPattern.test(trimmed);
}
