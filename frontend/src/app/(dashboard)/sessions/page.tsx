import { Suspense } from "react";
import { Card, CardContent } from "@/components/ui/card";
import { PageContainer } from "@/components/page-container";
import { SessionsPageContent } from "./sessions-page-content";

export default function SessionsPage() {
  return (
    <PageContainer size="wide">
      <Suspense
        fallback={
          <Card>
            <CardContent className="py-12 text-center text-[13px] text-muted-foreground">
              Loading sessions...
            </CardContent>
          </Card>
        }
      >
        <SessionsPageContent />
      </Suspense>
    </PageContainer>
  );
}
