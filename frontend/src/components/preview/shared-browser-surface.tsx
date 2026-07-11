"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Hand, Keyboard, Loader2, MousePointer2 } from "lucide-react";
import Image from "next/image";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { ErrorNotice } from "@/components/ui/error-notice";

export function mapSharedBrowserPoint(rect: Pick<DOMRect, "left" | "top" | "width" | "height">, viewport: { width: number; height: number }, clientX: number, clientY: number) {
  const scale = Math.min(rect.width / viewport.width, rect.height / viewport.height);
  const renderedWidth = viewport.width * scale;
  const renderedHeight = viewport.height * scale;
  const left = rect.left + (rect.width - renderedWidth) / 2;
  const top = rect.top + (rect.height - renderedHeight) / 2;
  if (clientX < left || clientX > left + renderedWidth || clientY < top || clientY > top + renderedHeight) return null;
  return { x: Math.round((clientX - left) / scale), y: Math.round((clientY - top) / scale) };
}

export function SharedBrowserSurface({ sessionId }: { sessionId: string }) {
  const queryClient = useQueryClient();
  const [path, setPath] = useState("");
  const control = useQuery({
    queryKey: ["preview-browser-control", sessionId],
    queryFn: () => api.sessions.preview.browserControl(sessionId),
    refetchInterval: 2000,
  });
  const observation = useQuery({
    queryKey: ["preview-browser-observation", sessionId],
    queryFn: () => api.sessions.preview.observeBrowser(sessionId),
    refetchInterval: 1500,
  });
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["preview-browser-observation", sessionId] });
    void queryClient.invalidateQueries({ queryKey: ["preview-browser-control", sessionId] });
  };
  const acquire = useMutation({ mutationFn: () => api.sessions.preview.acquireBrowserControl(sessionId), onSuccess: refresh });
  const release = useMutation({ mutationFn: () => api.sessions.preview.returnBrowserControl(sessionId), onSuccess: refresh });
  const act = useMutation({ mutationFn: (steps: Array<Record<string, unknown>>) => api.sessions.preview.actAsHuman(sessionId, steps), onSuccess: refresh });
  const image = observation.data?.screenshot?.png_base64;
  const viewport = observation.data?.viewport;
  const isHuman = control.data?.state === "human_control";
  const isLeaseOwner = isHuman && control.data?.is_lease_owner;
  const label = control.data?.state?.replaceAll("_", " ") ?? "connecting";
  const imageSrc = useMemo(() => image ? `data:image/png;base64,${image}` : undefined, [image]);

  const navigate = () => {
    const value = path.trim();
    if (value) act.mutate([{ action: "navigate", value: value.startsWith("/") ? value : `/${value}` }]);
  };

  return (
    <div className="overflow-hidden rounded-lg border bg-muted/30">
      <div className="flex flex-wrap items-center gap-2 border-b bg-card p-2">
        <Badge variant={isHuman ? "default" : "secondary"}>{label}</Badge>
        {control.data?.state === "waiting_for_handoff" && control.data.handoff_reason && (
          <span className="text-sm text-muted-foreground">{control.data.handoff_reason}</span>
        )}
        <div className="ml-auto flex items-center gap-2">
          {isLeaseOwner ? (
            <Button size="sm" variant="outline" onClick={() => release.mutate()} disabled={release.isPending}>Return control</Button>
          ) : !isHuman ? (
            <Button size="sm" onClick={() => acquire.mutate()} disabled={acquire.isPending}><Hand className="size-4" />Take control</Button>
          ) : <span className="text-sm text-muted-foreground">Another user has control</span>}
        </div>
      </div>
      {isLeaseOwner && (
        <div className="flex gap-2 border-b bg-card p-2">
          <Input aria-label="Preview path" value={path} onChange={(event) => setPath(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter") navigate(); }} placeholder="/path" />
          <Button variant="outline" onClick={navigate} disabled={act.isPending}>Go</Button>
        </div>
      )}
      {(control.error || observation.error || acquire.error || release.error || act.error) && (
        <ErrorNotice title="Shared browser unavailable" description={String(control.error || observation.error || acquire.error || release.error || act.error)} />
      )}
      <div className="relative mx-auto aspect-[16/10] max-h-[70vh] bg-background">
        {imageSrc ? (
          <>
            <Image src={imageSrc} alt={observation.data?.title || "Session browser"} fill unoptimized className="object-contain" draggable={false} />
            {isLeaseOwner && viewport && (
              <Button
                variant="ghost"
                className="absolute inset-0 h-full w-full cursor-crosshair rounded-none bg-transparent p-0 hover:bg-transparent"
                aria-label="Interact with session browser"
                onClick={(event) => {
                  const rect = event.currentTarget.getBoundingClientRect();
                  const point = mapSharedBrowserPoint(rect, viewport, event.clientX, event.clientY);
                  if (point) act.mutate([{ action: "click", ...point }]);
                }}
                onKeyDown={(event) => {
                  if (["Shift", "Control", "Alt", "Meta"].includes(event.key)) return;
                  event.preventDefault();
                  act.mutate([{ action: "press", value: event.key }]);
                }}
                onWheel={(event) => {
                  event.preventDefault();
                  if (!act.isPending) act.mutate([{ action: "scroll", value: String(Math.round(event.deltaY)) }]);
                }}
              ><span className="sr-only">Shared browser input surface</span></Button>
            )}
          </>
        ) : (
          <div className="flex h-full items-center justify-center gap-2 text-sm text-muted-foreground"><Loader2 className="size-4 animate-spin" />Connecting to the session browser…</div>
        )}
        {act.isPending && <div className="absolute bottom-3 right-3 flex items-center gap-2 rounded-md bg-background/90 px-3 py-2 text-sm shadow"><Loader2 className="size-4 animate-spin" />Applying input…</div>}
      </div>
      <div className="flex items-center gap-4 border-t bg-card px-3 py-2 text-xs text-muted-foreground">
        <span className="flex items-center gap-1"><MousePointer2 className="size-3" />Shared pointer</span>
        <span className="flex items-center gap-1"><Keyboard className="size-3" />Shared keyboard</span>
        {observation.data?.url && <span className="truncate">{observation.data.url}</span>}
      </div>
    </div>
  );
}
