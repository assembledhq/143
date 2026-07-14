"use client";

import { Suspense, useEffect, useRef } from "react";
import Link from "next/link";
import { useMutation } from "@tanstack/react-query";
import { useSearchParams } from "next/navigation";
import { CheckCircle2, Link2, TriangleAlert } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { PageContainer } from "@/components/page-container";

function ClaimExternalIdentity() {
  const params = useSearchParams();
  const token = params.get("token")?.trim() ?? "";
  const started = useRef(false);
  const claim = useMutation({ mutationFn: api.integrations.claimExternalIdentity });
  useEffect(() => {
    if (token && !started.current) {
      started.current = true;
      claim.mutate(token);
    }
  }, [token, claim]);

  const invalid = token === "";
  return (
    <PageContainer>
      <div className="mx-auto max-w-lg pt-12">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              {claim.isSuccess ? <CheckCircle2 className="h-5 w-5 text-green-600" /> : claim.isError || invalid ? <TriangleAlert className="h-5 w-5 text-destructive" /> : <Link2 className="h-5 w-5" />}
              {claim.isSuccess ? "Account connected" : claim.isError || invalid ? "Link unavailable" : "Connecting account"}
            </CardTitle>
            <CardDescription>
              {claim.isSuccess ? "Future work from this external account will use your 143 identity and personal capabilities." : claim.isError || invalid ? "This link is invalid, expired, already used, or belongs to another workspace." : "Verifying this one-time account link…"}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <Button asChild><Link href="/settings/account#external-identities">Open account settings</Link></Button>
          </CardContent>
        </Card>
      </div>
    </PageContainer>
  );
}

export default function ExternalIdentityClaimPage() {
  return <Suspense fallback={null}><ClaimExternalIdentity /></Suspense>;
}
