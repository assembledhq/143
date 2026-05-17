"use client";

import Image from "next/image";
import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import { useAuth, useAuthProviders } from "@/hooks/use-auth";

function hasCSRFCookie(): boolean {
  if (typeof document === "undefined") return false;
  return document.cookie.split("; ").some((cookie) => cookie.startsWith("csrf_token="));
}

function EmailAuthSkeleton() {
  return (
    <div data-testid="email-auth-skeleton" className="space-y-3 pt-1" aria-hidden="true">
      <div className="h-9 w-full rounded-md bg-muted animate-pulse" />
      <div className="space-y-2 pt-2">
        <div className="h-4 w-12 rounded bg-muted animate-pulse" />
        <div className="h-9 w-full rounded-md bg-muted animate-pulse" />
      </div>
      <div className="space-y-2">
        <div className="h-4 w-16 rounded bg-muted animate-pulse" />
        <div className="h-9 w-full rounded-md bg-muted animate-pulse" />
      </div>
      <div className="h-9 w-full rounded-md bg-muted animate-pulse" />
    </div>
  );
}

export default function LoginPage() {
  return (
    <Suspense>
      <LoginPageContent />
    </Suspense>
  );
}

function LoginPageContent() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const { isAuthenticated, isLoading: authLoading } = useAuth();
  const { providers, isLoading: providersLoading } = useAuthProviders();

  const invitation = searchParams.get("invitation") ?? undefined;
  const invitedEmail = searchParams.get("email") ?? "";
  const invitedGitHubUsername = searchParams.get("github_username") ?? "";
  const acceptanceMethod = searchParams.get("acceptance_method") ?? "";
  const invitedOrg = searchParams.get("org") ?? "";
  const isSwitchAccount = searchParams.get("switch_account") === "1";
  const isGitHubLockedInvite = acceptanceMethod === "github";
  const identityEmail = isGitHubLockedInvite ? "" : invitedEmail;
  const postEmailSignInHref = invitation
    ? `/invite/accept?token=${encodeURIComponent(invitation)}`
    : "/sessions";
  const inviteTarget = isGitHubLockedInvite && invitedGitHubUsername
    ? `@${invitedGitHubUsername}`
    : invitedEmail || (invitedGitHubUsername ? `@${invitedGitHubUsername}` : "");
  const [tab, setTab] = useState(searchParams.get("tab") === "signup" ? "signup" : "signin");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  // Sign in form
  const [signInEmail, setSignInEmail] = useState(identityEmail);
  const [signInPassword, setSignInPassword] = useState("");

  // Sign up form
  const [signUpName, setSignUpName] = useState("");
  const [signUpEmail, setSignUpEmail] = useState(identityEmail);
  const [signUpPassword, setSignUpPassword] = useState("");
  const emailAuthReady = hasCSRFCookie();
  const emailAuthPending = !emailAuthReady && (authLoading || providersLoading);

  useEffect(() => {
    if (!isSwitchAccount && !authLoading && isAuthenticated) {
      router.replace("/onboarding");
    }
  }, [authLoading, isAuthenticated, isSwitchAccount, router]);

  useEffect(() => {
    if (identityEmail) {
      setSignInEmail(identityEmail);
      setSignUpEmail(identityEmail);
    }
  }, [identityEmail]);

  const handleSignIn = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    if (!emailAuthReady) {
      setError("Secure email sign-in is still initializing. Try again in a moment.");
      return;
    }
    setLoading(true);
    try {
      await api.auth.loginEmail(signInEmail, signInPassword);
      window.location.href = postEmailSignInHref;
    } catch (err: unknown) {
      captureError(err, { feature: "auth-signin" });
      const message = err instanceof Error ? err.message : "Sign in failed";
      setError(message);
    } finally {
      setLoading(false);
    }
  };

  const handleSignUp = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    if (!emailAuthReady) {
      setError("Secure email sign-up is still initializing. Try again in a moment.");
      return;
    }
    setLoading(true);
    try {
      await api.auth.register(signUpEmail, signUpPassword, signUpName, invitation);
      window.location.href = "/sessions";
    } catch (err: unknown) {
      captureError(err, { feature: "auth-signup" });
      const message = err instanceof Error ? err.message : "Sign up failed";
      setError(message);
    } finally {
      setLoading(false);
    }
  };

  if (!isSwitchAccount && isAuthenticated) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background px-4">
        <div className="w-full max-w-sm rounded-lg border border-border bg-card p-6 space-y-4">
          <div className="text-center">
            <div className="mx-auto h-5 w-16 rounded bg-muted animate-pulse" />
          </div>
          <div className="space-y-2">
            <div className="h-9 w-full rounded-md bg-muted animate-pulse" />
            <div className="h-9 w-full rounded-md bg-muted animate-pulse" />
          </div>
          <div className="flex justify-center">
            <div className="h-3 w-36 rounded bg-muted animate-pulse" />
          </div>
          <div className="space-y-3 pt-2">
            <div className="h-4 w-12 rounded bg-muted animate-pulse" />
            <div className="h-9 w-full rounded-md bg-muted animate-pulse" />
            <div className="h-4 w-16 rounded bg-muted animate-pulse" />
            <div className="h-9 w-full rounded-md bg-muted animate-pulse" />
            <div className="h-9 w-full rounded-md bg-muted animate-pulse" />
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <Card className="w-full max-w-sm">
        <CardHeader className="text-center">
          <CardTitle className="text-lg font-semibold">143.dev</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {invitation && (invitedEmail || invitedGitHubUsername || invitedOrg) && (
            <div className="rounded-lg border border-border bg-muted/40 px-4 py-3 text-left">
              <div className="mb-2 flex items-center gap-2">
                <Badge variant="secondary">Invitation pending</Badge>
              </div>
              <div className="text-sm font-medium text-foreground">
                Join {invitedOrg || "this organization"}
              </div>
              <CardDescription className="mt-1 text-sm">
                Someone asked you to join
                {invitedOrg ? (
                  <>
                    {" "} <span className="font-medium text-foreground">{invitedOrg}</span>
                  </>
                ) : null}
                {inviteTarget ? (
                  <>
                    {" "}as <span className="font-medium text-foreground">{inviteTarget}</span>
                  </>
                ) : null}
                {isGitHubLockedInvite
                  ? ". Continue with GitHub using the invited account to accept the invitation."
                  : ". Sign in if you already have an account, or create one to accept the invitation."}
              </CardDescription>
            </div>
          )}

          {providers?.demo && providers.demo_email && providers.demo_password && (
            <div
              className="rounded-md border border-amber-300/50 bg-amber-50/50 px-3 py-2 text-sm text-muted-foreground dark:border-amber-500/30 dark:bg-amber-950/30"
              data-testid="demo-banner"
            >
              <div className="font-medium text-foreground">Demo environment</div>
              <div className="mt-1">
                Sign in with <code className="font-mono">{providers.demo_email}</code>
                {" / "}
                <code className="font-mono">{providers.demo_password}</code>.
              </div>
              <div className="mt-1 text-xs">
                Data resets when the preview recycles. GitHub actions are stubbed.
              </div>
            </div>
          )}

          {/* Social login buttons */}
          <div className="space-y-2">
            {providers?.github !== false && (
              <Button
                variant="outline"
                className="w-full"
                onClick={() => api.auth.login(invitation)}
                aria-label="Continue with GitHub"
              >
                <Image src="/integrations/github.svg" alt="" width={20} height={20} className="mr-2 h-5 w-5 dark:invert" aria-hidden="true" />
                Continue with GitHub
              </Button>
            )}
            {providers?.google && (
              <Button
                variant="outline"
                className="w-full"
                onClick={() => api.auth.loginGoogle(invitation)}
                aria-label="Continue with Google"
              >
                <Image src="/integrations/google.svg" alt="" width={20} height={20} className="mr-2 h-5 w-5" aria-hidden="true" />
                Continue with Google
              </Button>
            )}
          </div>

          {(providers?.github !== false || providers?.google) && (
            <div className="relative">
              <div className="absolute inset-0 flex items-center">
                <span className="w-full border-t" />
              </div>
              <div className="relative flex justify-center text-xs uppercase">
                <span className="bg-card px-2 text-muted-foreground">
                  or continue with email
                </span>
              </div>
            </div>
          )}

          {error && (
            <div className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive" role="alert">
              {error}
            </div>
          )}

          {!emailAuthReady && !emailAuthPending && (
            <CardDescription className="text-center text-xs">
              Secure email authentication could not be initialized. Refresh and try again.
            </CardDescription>
          )}

          {emailAuthPending ? (
            <EmailAuthSkeleton />
          ) : (
            <Tabs value={tab} onValueChange={setTab}>
              <TabsList className="w-full">
                <TabsTrigger value="signin" className="flex-1">Sign in</TabsTrigger>
                <TabsTrigger value="signup" className="flex-1">Sign up</TabsTrigger>
              </TabsList>

              <TabsContent value="signin">
                <form onSubmit={handleSignIn} className="space-y-3 pt-2">
                  <div className="space-y-1.5">
                    <Label htmlFor="signin-email">Email</Label>
                    <Input
                      id="signin-email"
                      type="email"
                      placeholder="you@example.com"
                      value={signInEmail}
                      onChange={(e) => setSignInEmail(e.target.value)}
                      readOnly={Boolean(invitation && identityEmail)}
                      required
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="signin-password">Password</Label>
                    <Input
                      id="signin-password"
                      type="password"
                      value={signInPassword}
                      onChange={(e) => setSignInPassword(e.target.value)}
                      required
                    />
                  </div>
                  <Button
                    type="submit"
                    className="w-full"
                    loading={loading}
                    disabled={loading || !emailAuthReady}
                  >
                    Sign in
                  </Button>
                </form>
              </TabsContent>

              <TabsContent value="signup">
                <form onSubmit={handleSignUp} className="space-y-3 pt-2">
                  <div className="space-y-1.5">
                    <Label htmlFor="signup-name">Name</Label>
                    <Input
                      id="signup-name"
                      type="text"
                      placeholder="Your name"
                      value={signUpName}
                      onChange={(e) => setSignUpName(e.target.value)}
                      required
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="signup-email">Email</Label>
                    <Input
                      id="signup-email"
                      type="email"
                      placeholder="you@example.com"
                      value={signUpEmail}
                      onChange={(e) => setSignUpEmail(e.target.value)}
                      readOnly={Boolean(invitation && identityEmail)}
                      required
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="signup-password">Password</Label>
                    <Input
                      id="signup-password"
                      type="password"
                      placeholder="At least 8 characters"
                      value={signUpPassword}
                      onChange={(e) => setSignUpPassword(e.target.value)}
                      required
                      minLength={8}
                    />
                  </div>
                  <Button
                    type="submit"
                    className="w-full"
                    loading={loading}
                    disabled={loading || !emailAuthReady}
                  >
                    Create account
                  </Button>
                </form>
              </TabsContent>
            </Tabs>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
