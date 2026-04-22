  return { ...node, children: newChildren };
}

const TreeDirectory = memo(function TreeDirectory({
  node,
  activeFileIndex,
}: FileTreeProps) {
  const [filter, setFilter] = useState("");

  const filteredFiles = useMemo(() => {
    if (!filter.trim()) return files;
    const q = filter.toLowerCase();
    return files.filter((f) => f.newPath.toLowerCase().includes(q));
  }, [files, filter]);

  const tree = useMemo(
    () => flattenSingleChildDirs(buildTree(filteredFiles)),
    [filteredFiles]
  );

  return (
    <div className="flex flex-col h-full">
      <div className="px-4 pb-3">
      <div className="flex-1 overflow-y-auto scrollbar-hide px-3 pb-2">
        <TreeDirectory
          node={tree}
          activeFileIndex={activeFileIndex}
          onFileSelect={onFileSelect}
        />
      </div>
    </div>
