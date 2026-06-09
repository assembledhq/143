export function canCopyToClipboard(): boolean {
  return (
    typeof navigator !== "undefined" &&
    typeof navigator.clipboard?.writeText === "function"
  );
}

export async function copyTextToClipboard(value: string): Promise<void> {
  if (!canCopyToClipboard()) {
    throw new Error("Clipboard API is unavailable");
  }
  await navigator.clipboard.writeText(value);
}
