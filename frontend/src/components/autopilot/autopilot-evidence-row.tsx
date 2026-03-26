"use client";

interface AutopilotEvidenceRowProps {
  evidence: Array<{ label: string; value: string }>;
}

export function AutopilotEvidenceRow({ evidence }: AutopilotEvidenceRowProps) {
  return (
    <div className="grid gap-3 rounded-2xl border border-border/60 bg-muted/20 px-5 py-4 sm:grid-cols-2 xl:grid-cols-4">
      {evidence.map((item) => (
        <div key={item.label} className="space-y-1">
          <p className="text-xs uppercase tracking-[0.12em] text-muted-foreground">{item.label}</p>
          <p className="text-base font-medium text-foreground">{item.value}</p>
        </div>
      ))}
    </div>
  );
}
