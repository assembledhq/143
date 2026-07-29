// browserTimezone returns the viewer's IANA zone, or "UTC" if the browser
// can't resolve one (older Safari/headless). Resolving lazily keeps SSR safe
// — pages should call this inside a useState initializer.
//
// Schedule conversion, parsing, and formatting live in
// @/lib/automation-schedule; this module is only the browser-zone probe.
export function browserTimezone(): string {
  try {
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
    return tz || "UTC";
  } catch {
    return "UTC";
  }
}
