"use client";

import { Suspense, useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useRouter, useSearchParams } from "next/navigation";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { ApiError, api } from "@/lib/api";
import { setActiveOrgId } from "@/lib/active-org";
import { captureError } from "@/lib/errors";

export default function VerifyEmailPage() {
  return (
    <Suspense>
      <VerifyEmailContent />
    </Suspense>
  );
}

function VerifyEmailContent() {
  const router = useRouter();
  const queryClient = useQueryClient();
  const searchParams = useSearchParams();
  const token = searchParams.get("token");

  const [status, setStatus] = useState<"loading" | "verified" | "joined" | "error">(
    token ? "loading" : "error",
  );
  const [errorMessage, setErrorMessage] = useState(
    token ? "" : "No verification token provided.",
  );
  const [joinedOrgName, setJoinedOrgName] = useState("");
  // The confirm endpoint consumes the token (single-use); React 18 strict
  // mode double-invokes effects in dev, so guard against firing twice.
  const confirmedRef = useRef(false);

  useEffect(() => {
    if (!token || confirmedRef.current) {
      return;
    }
    confirmedRef.current = true;

    async function confirm() {
      try {
        // Warm the CSRF double-submit cookie before the POST: this page is
        // opened cold from an email link, often on a device that has never
        // touched the app, so no prior safe-method request has issued the
        // cookie and the confirm would otherwise 403 with CSRF_FAILED.
        await api.auth.providers().catch(() => undefined);
        const res = await api.auth.confirmEmailVerification(token!);
        const joined = res.data.joined_org;
        if (joined) {
          setJoinedOrgName(joined.org_name);
          setStatus("joined");
          // Land the user in their team's workspace, mirroring OAuth
          // domain capture. Clearing the cache makes every query refetch
          // under the new active org.
          setActiveOrgId(joined.org_id);
          queryClient.clear();
          return;
        }
        setStatus("verified");
      } catch (err) {
        // UNAUTHORIZED never happens (public route); 410 means the link is
        // stale or superseded.
        if (!(err instanceof ApiError)) {
          captureError(err, { feature: "verify-email" });
        }
        setStatus("error");
        setErrorMessage(
          err instanceof Error && err.message
            ? err.message
            : "Something went wrong. Please try again.",
        );
      }
    }

    void confirm();
  }, [token, queryClient]);

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <Card className="w-full max-w-sm">
        <CardHeader className="space-y-1 text-center">
          <CardTitle className="text-lg font-semibold">
            {status === "joined" ? `Welcome to ${joinedOrgName}` : "Verify your email"}
          </CardTitle>
          {status === "joined" && (
            <CardDescription>
              Your email is verified and you&apos;ve joined your team&apos;s workspace.
            </CardDescription>
          )}
        </CardHeader>
        <CardContent className="space-y-4">
          {status === "loading" && (
            <p className="text-center text-sm text-muted-foreground">Verifying your email...</p>
          )}

          {status === "verified" && (
            <div className="space-y-4">
              <p className="text-center text-sm text-muted-foreground">
                Your email address is verified.
              </p>
              <Button className="w-full" onClick={() => router.replace("/sessions")}>
                Continue
              </Button>
            </div>
          )}

          {status === "joined" && (
            <Button
              className="w-full"
              data-testid="verify-email-continue"
              onClick={() => router.replace("/sessions")}
            >
              Go to {joinedOrgName}
            </Button>
          )}

          {status === "error" && (
            <div className="space-y-4">
              <div className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
                {errorMessage}
              </div>
              <p className="text-center text-xs text-muted-foreground">
                You can request a new link from the workspace switcher after signing in.
              </p>
              <Button
                variant="outline"
                className="w-full"
                onClick={() => router.push("/login")}
              >
                Go to sign in
              </Button>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
