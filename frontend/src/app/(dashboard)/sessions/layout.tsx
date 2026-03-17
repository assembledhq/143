"use client";

import { SessionSidebar } from "./session-sidebar";

export default function SessionsLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-[calc(100vh-theme(spacing.6)*2)] -mx-8 -my-6 lg:-mx-10">
      {/* Session list sidebar */}
      <SessionSidebar />

      {/* Main content area */}
      <div className="flex-1 min-w-0 overflow-auto">
        {children}
      </div>
    </div>
  );
}
