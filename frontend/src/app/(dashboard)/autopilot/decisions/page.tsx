"use client";

import Link from "next/link";
import { ArrowLeft, Zap } from "lucide-react";
import { PageContainer } from "@/components/page-container";
import { DecisionsView } from "@/components/pm/decisions-view";

export default function DecisionsPage() {
  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <div className="flex items-center gap-3">
          <Link
            href="/autopilot"
            className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground transition-colors"
          >
            <ArrowLeft className="h-4 w-4" />
          </Link>
          <div className="flex items-center gap-2">
            <Zap className="h-5 w-5 text-primary" />
            <h1 className="text-2xl font-bold tracking-tight text-foreground">Decision History</h1>
          </div>
        </div>
        <DecisionsView />
      </div>
    </PageContainer>
  );
}
