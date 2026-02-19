import { Suspense } from "react";
import { Card, CardContent } from "@/components/ui/card";
import { RunsPageContent } from "./runs-page-content";

export default function RunsPage() {
  return (
    <Suspense
      fallback={
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            Loading runs...
          </CardContent>
        </Card>
      }
    >
      <RunsPageContent />
    </Suspense>
  );
}
