"use client";

import { useQuery } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { Lightbulb, ArrowRight } from "lucide-react";

export function AutopilotProposalCard() {
  const router = useRouter();

  const { data: summaryData } = useQuery({
    queryKey: ["proposalSummary"],
    queryFn: () => api.projects.proposalSummary(),
    refetchInterval: 30000,
  });

  const { data: topProposalData } = useQuery({
    queryKey: ["projects", "proposed", "top"],
    queryFn: () => api.projects.list({ status: "proposed", limit: 1 }),
    refetchInterval: 30000,
  });

  const count = summaryData?.data?.count ?? 0;
  const topProposal = topProposalData?.data?.[0];

  if (count === 0) return null;

  return (
    <Card className="border-purple-200 dark:border-purple-800/50 bg-purple-50/50 dark:bg-purple-950/20">
      <CardContent className="py-3 px-4">
        <div className="flex items-center justify-between gap-3">
          <div className="flex items-center gap-3 min-w-0">
            <div className="flex items-center justify-center h-8 w-8 rounded-full bg-purple-100 dark:bg-purple-900/50 shrink-0">
              <Lightbulb className="h-4 w-4 text-purple-600 dark:text-purple-400" />
            </div>
            <div className="min-w-0">
              <p className="text-sm font-medium">
                PM found {count} strategic {count === 1 ? "opportunity" : "opportunities"}
              </p>
              {topProposal && (
                <p className="text-xs text-muted-foreground truncate">
                  Top proposal: {topProposal.title}
                </p>
              )}
            </div>
          </div>
          <Button
            variant="outline"
            size="sm"
            onClick={() => router.push("/projects?filter=proposed")}
            className="shrink-0"
          >
            Review proposals
            <ArrowRight className="h-3.5 w-3.5 ml-1" />
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
