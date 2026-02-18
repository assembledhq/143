interface DiffViewerProps {
  diff: string;
}

export function DiffViewer({ diff }: DiffViewerProps) {
  const lines = diff.split("\n");

  return (
    <pre className="overflow-x-auto rounded-md border border-border text-xs font-mono leading-relaxed">
      {lines.map((line, i) => {
        let className = "px-3 py-0.5";
        if (line.startsWith("+")) {
          className += " bg-green-50 text-green-800";
        } else if (line.startsWith("-")) {
          className += " bg-red-50 text-red-800";
        } else if (line.startsWith("@@")) {
          className += " bg-blue-50 text-blue-700";
        } else {
          className += " text-foreground";
        }

        return (
          <div key={i} className={className}>
            {line || "\u00A0"}
          </div>
        );
      })}
    </pre>
  );
}
