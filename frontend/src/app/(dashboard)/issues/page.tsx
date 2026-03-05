import { Suspense } from "react";
import { Card, CardContent } from "@/components/ui/card";
import { PageContainer } from "@/components/page-container";
import { IssuesPageContent } from "./issues-page-content";

export default function IssuesPage() {
  return (
    <PageContainer size="wide">
      <Suspense
        fallback={
          <Card>
            <CardContent className="py-12 text-center text-sm text-muted-foreground">
              Loading issues...
            </CardContent>
          </Card>
        }
      >
        <IssuesPageContent />
      </Suspense>
    </PageContainer>
  );
}
