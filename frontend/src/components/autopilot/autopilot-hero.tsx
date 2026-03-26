"use client";

import { Card, CardContent } from "@/components/ui/card";

interface AutopilotHeroProps {
  title: string;
  body: string;
}

export function AutopilotHero({ title, body }: AutopilotHeroProps) {
  return (
    <Card className="border-border/70 shadow-sm">
      <CardContent className="space-y-4 py-8">
        <div className="space-y-2">
          <p className="text-sm font-medium text-foreground">{title}</p>
          <p className="max-w-3xl text-lg leading-8 text-foreground">{body}</p>
        </div>
      </CardContent>
    </Card>
  );
}
