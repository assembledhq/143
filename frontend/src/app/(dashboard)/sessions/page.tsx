import { Suspense } from "react";
import { Card, CardContent } from "@/components/ui/card";
import { SessionsPageContent } from "./sessions-page-content";

export default function SessionsPage() {
  return (
    <Suspense
      fallback={
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            Loading sessions...
          </CardContent>
        </Card>
      }
    >
      <SessionsPageContent />
    </Suspense>
  );
}
