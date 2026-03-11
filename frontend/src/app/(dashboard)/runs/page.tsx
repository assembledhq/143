import { Suspense } from "react";
import { Card, CardContent } from "@/components/ui/card";
import { PageContainer } from "@/components/page-container";
import { RunsPageContent } from "./runs-page-content";

export default function RunsPage() {
  return (
    <PageContainer size="wide">
      <Suspense
        fallback={
          <Card>
            <CardContent className="py-12 text-center text-[13px] text-muted-foreground">
              Loading runs...
            </CardContent>
          </Card>
        }
      >
        <RunsPageContent />
      </Suspense>
    </PageContainer>
  );
}
