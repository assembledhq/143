"use client";

import Link from "next/link";
import { Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/hooks/use-auth";

export default function ProjectsPage() {
  const { user } = useAuth();
  const canManage = user?.role === "admin" || user?.role === "member";

  return (
    <div className="hidden md:flex h-full items-center justify-center p-8">
      <div className="max-w-md text-center space-y-4">
        <h1 className="text-lg font-semibold text-foreground">Projects</h1>
        <p className="text-sm text-muted-foreground">
          Start a new project, or open one from the list to continue.
        </p>
        {canManage && <Button asChild>
          <Link href="/projects/new">
            <Plus className="h-4 w-4" />
            New project
          </Link>
        </Button>}
      </div>
    </div>
  );
}
