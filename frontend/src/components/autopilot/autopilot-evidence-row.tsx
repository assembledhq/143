"use client";

interface AutopilotEvidenceRowProps {
  evidence: Array<{ label: string; value: string }>;
}

export function AutopilotEvidenceRow({ evidence }: AutopilotEvidenceRowProps) {
  return (
    <div className="grid grid-cols-3 gap-4">
      {evidence.map((item) => (
        <div key={item.label} className="rounded-xl border border-border/60 bg-surface-pane/70 px-5 py-4 text-center">
          <p className="text-2xl font-semibold tracking-tight text-foreground">{item.value}</p>
          <p className="mt-1 text-xs uppercase tracking-[0.12em] text-muted-foreground">{item.label}</p>
        </div>
      ))}
    </div>
  );
}
