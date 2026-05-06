export function getClipboardFiles(data: DataTransfer | null | undefined): File[] {
  if (!data) {
    return [];
  }

  const itemFiles = Array.from(data.items ?? [])
    .filter((item) => item.kind === "file")
    .map((item) => item.getAsFile())
    .filter((file): file is File => file instanceof File);
  if (itemFiles.length > 0) {
    return itemFiles;
  }

  return Array.from(data.files ?? []);
}
