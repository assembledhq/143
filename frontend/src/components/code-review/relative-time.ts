export function formatRelativeTime(dateString: string): string {
  const diffMinutes = Math.floor(
    (Date.now() - new Date(dateString).getTime()) / 60_000
  );

  if (diffMinutes < 1) return "just now";
  if (diffMinutes < 60) return `${diffMinutes}m ago`;

  const diffHours = Math.floor(diffMinutes / 60);
  if (diffHours < 24) return `${diffHours}h ago`;

  return `${Math.floor(diffHours / 24)}d ago`;
}
