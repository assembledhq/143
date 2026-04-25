import Link from "next/link";
import { Plus } from "lucide-react";
import { Button } from "@/components/ui/button";

export default function SessionsPage() {
  return (
    <div className="hidden md:flex h-full items-center justify-center p-8">
      <div className="max-w-md text-center space-y-4">
        <h1 className="text-lg font-semibold text-foreground">Sessions</h1>
        <p className="text-sm text-muted-foreground">
          Start a new session, or open one from the list to keep working.
        </p>
        <Button asChild>
          <Link href="/sessions/new">
            <Plus className="h-4 w-4" />
            New session
          </Link>
        </Button>
      </div>
    </div>
  );
}
