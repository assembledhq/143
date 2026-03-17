import { MessageSquare } from "lucide-react";

export default function SessionsPage() {
  return (
    <div className="flex items-center justify-center h-full">
      <div className="text-center space-y-3">
        <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted mx-auto">
          <MessageSquare className="h-6 w-6 text-muted-foreground" />
        </div>
        <div>
          <p className="text-sm font-medium text-foreground">Select a session</p>
          <p className="text-[13px] text-muted-foreground mt-1">
            Choose a session from the sidebar to view its details and chat history.
          </p>
        </div>
      </div>
    </div>
  );
}
