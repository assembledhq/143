"use client";

import { Suspense, useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useRouter, useSearchParams } from "next/navigation";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ErrorText } from "@/components/ui/error-notice";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { setActiveOrgId } from "@/lib/active-org";
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
  const queryClient = useQueryClient();
  const searchParams = useSearchParams();
  const token = searchParams.get("token");
  const isMissingToken = !token;

  const [status, setStatus] = useState<
    "loading" | "error" | "register" | "login" | "claiming"
  >(isMissingToken ? "error" : "loading");
  const [errorMessage, setErrorMessage] = useState(
    isMissingToken ? "No invitation token provided." : ""
  );
  const [errorAction, setErrorAction] = useState<"login" | "switch-account">("login");
  const [orgName, setOrgName] = useState("");
  const [invitedEmail, setInvitedEmail] = useState("");
  const [invitedGitHubUsername, setInvitedGitHubUsername] = useState("");
  const [acceptanceMethod, setAcceptanceMethod] = useState("");

  const isUnauthorized = (err: unknown) =>
    typeof err === "object" &&
    err !== null &&
    (err as { code?: unknown }).code === "UNAUTHORIZED";

  useEffect(() => {
    if (!token) {
      return;
    }
    const inviteToken = token;

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
          setErrorAction("login");
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
        if (data?.github_username) {
          setInvitedGitHubUsername(data.github_username);
        }
        if (data?.acceptance_method) {
          setAcceptanceMethod(data.acceptance_method);
        }

        try {
          await api.auth.me();
          setStatus("claiming");
          const claim = await api.auth.claimInvitation(inviteToken);
          setActiveOrgId(claim.data.org_id);
          queryClient.clear();
          router.replace("/onboarding");
          return;
        } catch (err) {
          if (!isUnauthorized(err)) {
            captureError(err, { feature: "invite-claim" });
            setStatus("error");
            setErrorAction(
              typeof err === "object" &&
                err !== null &&
                (err as { code?: unknown }).code === "INVITE_MISMATCH"
                ? "switch-account"
                : "login"
            );
            setErrorMessage(
              err instanceof Error ? err.message : "Something went wrong. Please try again."
            );
            return;
          }
        }

        if (data?.action === "login" || data?.action === "register") {
          setStatus(data.action);
          return;
        }

        router.replace("/onboarding");
      } catch (err) {
        captureError(err, { feature: "invite-accept" });
        setStatus("error");
        setErrorMessage("Something went wrong. Please try again.");
      }
    }

    acceptInvitation();
  }, [token, router, queryClient]);

  const invitationSummary = orgName || invitedEmail || invitedGitHubUsername;
  const joinLabel = orgName ? `Join ${orgName}` : "Accept invitation";
  const isGitHubLockedInvite = acceptanceMethod === "github";
  const accountTarget = isGitHubLockedInvite && invitedGitHubUsername
    ? `@${invitedGitHubUsername}`
    : invitedEmail || (invitedGitHubUsername ? `@${invitedGitHubUsername}` : "");
  const invitationParams = `${invitedEmail ? `&email=${encodeURIComponent(invitedEmail)}` : ""}${
    invitedGitHubUsername
      ? `&github_username=${encodeURIComponent(invitedGitHubUsername)}`
      : ""
  }${acceptanceMethod ? `&acceptance_method=${encodeURIComponent(acceptanceMethod)}` : ""}${orgName ? `&org=${encodeURIComponent(orgName)}` : ""}`;
  const loginHref = token
    ? `/login?invitation=${encodeURIComponent(token)}${invitationParams}`
    : "/login";
  const switchAccountHref = `${loginHref}${token ? "&" : "?"}switch_account=1`;

  const handleDifferentAccountSignIn = async () => {
    setActiveOrgId(null);
    queryClient.clear();
    try {
      await api.auth.logout();
    } catch (err) {
      captureError(err, { feature: "invite-claim-switch-account" });
    }
    router.push(switchAccountHref);
  };

  const handleErrorCta = async () => {
    if (errorAction === "switch-account") {
      await handleDifferentAccountSignIn();
      return;
    }
    router.push(loginHref);
  };

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <Card className="w-full max-w-sm">
        <CardHeader className="space-y-3 text-center">
          {invitationSummary ? (
            <div className="flex justify-center">
              <Badge variant="secondary">Invitation pending</Badge>
            </div>
          ) : null}
          <div className="space-y-1">
            <CardTitle className="text-lg font-semibold">
              {invitationSummary ? joinLabel : "143.dev"}
            </CardTitle>
            {invitationSummary ? (
              <CardDescription>
                Someone invited you to collaborate
                {orgName ? (
                  <>
                    {" "}in <span className="font-medium text-foreground">{orgName}</span>
                  </>
                ) : null}
                {accountTarget ? (
                  <>
                    {" "}as <span className="font-medium text-foreground">{accountTarget}</span>
                  </>
                ) : null}.
              </CardDescription>
            ) : null}
          </div>
        </CardHeader>
        <CardContent className="space-y-4">
          {(status === "loading" || status === "claiming") && (
            <p className="text-center text-sm text-muted-foreground">
              {status === "claiming" ? "Joining organization..." : "Verifying invitation..."}
            </p>
          )}

          {status === "error" && (
            <div className="space-y-4">
              <ErrorText className="rounded-md bg-destructive/10 px-3 py-2 text-sm">
                {errorMessage}
              </ErrorText>
              <Button
                variant="outline"
                className="w-full"
                onClick={() => void handleErrorCta()}
              >
                {errorAction === "switch-account" ? "Sign in with a different account" : "Go to sign in"}
              </Button>
            </div>
          )}

          {status === "register" && (
            <div className="space-y-4">
              <p className="text-center text-sm text-muted-foreground">
                Create an account to accept this invitation
                {orgName ? (
                  <>
                    {" "}for <span className="font-medium text-foreground">{orgName}</span>
                  </>
                ) : (
                  "."
                )}
                {accountTarget ? (
                  <>
                    {" "}We&apos;ll join you as <span className="font-medium text-foreground">{accountTarget}</span>.
                  </>
                ) : null}
              </p>
              <Button
                className="w-full"
                onClick={() =>
                  router.push(
                    `/login?tab=signup&invitation=${encodeURIComponent(token!)}${invitationParams}`
                  )
                }
              >
                {orgName ? `Create account to join ${orgName}` : "Create account"}
              </Button>
              <Button
                variant="outline"
                className="w-full"
                onClick={() => router.push(loginHref)}
              >
                I already have an account
              </Button>
            </div>
          )}

          {status === "login" && (
            <div className="space-y-4">
              <p className="text-center text-sm text-muted-foreground">
                Sign in to accept this invitation
                {orgName ? (
                  <>
                    {" "}for <span className="font-medium text-foreground">{orgName}</span>
                  </>
                ) : (
                  "."
                )}
                {accountTarget ? (
                  <>
                    {" "}Use the invited account <span className="font-medium text-foreground">{accountTarget}</span>.
                  </>
                ) : null}
              </p>
              <Button
                className="w-full"
                onClick={() => router.push(loginHref)}
              >
                {orgName ? `Sign in to join ${orgName}` : "Sign in"}
              </Button>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
