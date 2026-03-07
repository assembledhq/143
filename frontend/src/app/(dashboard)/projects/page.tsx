import { Suspense } from "react";
import { Card, CardContent } from "@/components/ui/card";
import { PageContainer } from "@/components/page-container";
import { ProjectsPageContent } from "./projects-page-content";

export default function ProjectsPage() {
  return (
    <PageContainer size="wide">
      <Suspense
        fallback={
          <Card>
            <CardContent className="py-12 text-center text-sm text-muted-foreground">
              Loading projects...
            </CardContent>
          </Card>
        }
      >
        <ProjectsPageContent />
      </Suspense>
    </PageContainer>
  );
}
