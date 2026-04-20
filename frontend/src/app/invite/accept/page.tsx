"use client";

import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { captureError } from "@/lib/errors";

const API_BASE = process.env.NEXT_PUBLIC_API_URL || "";

export default function AcceptInvitationPage() {
  return (
    <Suspense>
      <AcceptInvitationContent />
    </Suspense>
  );
}

function AcceptInvitationContent() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const token = searchParams.get("token");
  const isMissingToken = !token;

  const [status, setStatus] = useState<
    "loading" | "error" | "register" | "login"
  >(isMissingToken ? "error" : "loading");
  const [errorMessage, setErrorMessage] = useState(
    isMissingToken ? "No invitation token provided." : ""
  );
  const [orgName, setOrgName] = useState("");
  const [invitedEmail, setInvitedEmail] = useState("");

  useEffect(() => {
    if (!token) {
      return;
    }

    async function acceptInvitation() {
      try {
        const res = await fetch(`${API_BASE}/api/v1/team/invitations/accept`, {
          method: "POST",
          credentials: "include",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ token }),
        });

        const body = await res.json().catch(() => ({}));

        if (!res.ok) {
          setStatus("error");
          setErrorMessage(
            body?.error?.message || "This invitation is no longer valid."
          );
          return;
        }

        const data = body?.data;
        if (data?.org_name) {
          setOrgName(data.org_name);
        }
        if (data?.email) {
          setInvitedEmail(data.email);
        }
        if (data?.action === "login" || data?.action === "register") {
          setStatus(data.action);
        } else {
          // Fallback: redirect to onboarding (which redirects to sessions if setup is complete)
          router.replace("/onboarding");
        }
      } catch (err) {
        captureError(err, { feature: "invite-accept" });
        setStatus("error");
        setErrorMessage("Something went wrong. Please try again.");
      }
    }

    acceptInvitation();
  }, [token, router]);

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <Card className="w-full max-w-sm">
        <CardHeader className="text-center">
          <CardTitle className="text-lg font-semibold">143.dev</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {status === "loading" && (
            <p className="text-center text-sm text-muted-foreground">
              Verifying invitation...
            </p>
          )}

          {status === "error" && (
            <div className="space-y-4">
              <div className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
                {errorMessage}
              </div>
              <Button
                variant="outline"
                className="w-full"
                onClick={() => router.push("/login")}
              >
                Go to Login
              </Button>
            </div>
          )}

          {status === "register" && (
            <div className="space-y-4">
              <p className="text-center text-sm text-muted-foreground">
                You&apos;ve been invited to join{" "}
                <span className="font-medium text-foreground">
                  {orgName}
                </span>
                {invitedEmail ? (
                  <>
                    {" "}as <span className="font-medium text-foreground">{invitedEmail}</span>. Create an account to get started.
                  </>
                ) : (
                  ". Create an account to get started."
                )}
              </p>
              <Button
                className="w-full"
                onClick={() =>
                  router.push(
                    `/login?tab=signup&invitation=${encodeURIComponent(token!)}${
                      invitedEmail
                        ? `&email=${encodeURIComponent(invitedEmail)}`
                        : ""
                    }${
                      orgName ? `&org=${encodeURIComponent(orgName)}` : ""
                    }`
                  )
                }
              >
                Create account
              </Button>
              <Button
                variant="outline"
                className="w-full"
                onClick={() =>
                  router.push(
                    `/login?invitation=${encodeURIComponent(token!)}${
                      invitedEmail
                        ? `&email=${encodeURIComponent(invitedEmail)}`
                        : ""
                    }${
                      orgName ? `&org=${encodeURIComponent(orgName)}` : ""
                    }`
                  )
                }
              >
                Sign in to existing account
              </Button>
            </div>
          )}

          {status === "login" && (
            <div className="space-y-4">
              <p className="text-center text-sm text-muted-foreground">
                You&apos;ve been invited to join{" "}
                <span className="font-medium text-foreground">
                  {orgName}
                </span>
                {invitedEmail ? (
                  <>
                    {" "}as <span className="font-medium text-foreground">{invitedEmail}</span>. Sign in to accept the invitation.
                  </>
                ) : (
                  ". Sign in to accept the invitation."
                )}
              </p>
              <Button
                className="w-full"
                onClick={() =>
                  router.push(
                    `/login?invitation=${encodeURIComponent(token!)}${
                      invitedEmail
                        ? `&email=${encodeURIComponent(invitedEmail)}`
                        : ""
                    }${
                      orgName ? `&org=${encodeURIComponent(orgName)}` : ""
                    }`
                  )
                }
              >
                Sign in
              </Button>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
