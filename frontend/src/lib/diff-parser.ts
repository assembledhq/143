    for (let i = 0; i < lines.length; i++) {
      const line = lines[i];
      if (line.startsWith("--- ")) {
        oldPath = normalizeDiffPath(line.slice(4));
      } else if (line.startsWith("+++ ")) {
        newPath = normalizeDiffPath(line.slice(4));
        headerEnd = i + 1;
        break;
      }
  return files;
}

function normalizeDiffPath(path: string): string {
  if (path === "/dev/null") {
    return "";
  }
  if (path.startsWith("a/") || path.startsWith("b/")) {
    return path.slice(2);
  }
  return path;
}

/**
 * Serialize hunks to a stable string for comparison between passes.
 * Two files with identical serialized hunks have the same diff content.
