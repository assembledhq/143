import { GitBranch, AlertCircle, Zap } from "lucide-react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { PageHeader } from "@/components/page-header";

const steps = [
  {
    icon: GitBranch,
    title: "Connect GitHub",
    description: "Link your GitHub account so 143 can open pull requests on your repositories.",
    action: { label: "Connect", href: "/settings" },
  },
  {
    icon: AlertCircle,
    title: "Connect an issue tracker",
    description: "Connect Sentry, Linear, or another source so 143 knows what to fix.",
    action: { label: "Set up", href: "/settings" },
  },
  {
    icon: Zap,
    title: "Watch fixes ship",
    description: "Once connected, 143 picks up issues, generates fixes, and opens PRs automatically.",
    action: null,
  },
];

export default function Overview() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Overview"
        description="Get started by connecting your tools."
      />

      <div className="space-y-3">
        {steps.map((step, i) => (
          <Card key={i} className="py-0">
            <CardContent className="flex items-start gap-4 py-4">
              <div className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-muted">
                <step.icon className="h-4 w-4 text-muted-foreground" />
              </div>
              <div className="flex-1 min-w-0">
                <p className="text-sm font-medium text-foreground">{step.title}</p>
                <p className="mt-0.5 text-sm text-muted-foreground">{step.description}</p>
              </div>
              {step.action && (
                <Button variant="outline" size="sm" asChild>
                  <Link href={step.action.href}>{step.action.label}</Link>
                </Button>
              )}
            </CardContent>
          </Card>
        ))}
      </div>
    </div>
  );
}
