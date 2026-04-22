        {/* Session header bar */}
        <div className="border-b border-border px-4 py-3 bg-background flex items-center justify-between shrink-0">
          <div className="min-w-0 flex-1 flex items-center gap-2">
            <h1 className="text-sm font-medium text-foreground truncate">
              {sessionTitle(session)}
            </h1>
            <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium shrink-0 ${status.color}`}>
