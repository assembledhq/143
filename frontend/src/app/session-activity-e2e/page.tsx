import { notFound } from "next/navigation";
import { Suspense } from "react";
import { SessionActivityE2EFixture } from "./session-activity-e2e-fixture";

export default function SessionActivityE2EPage() {
  if (process.env.SESSION_ACTIVITY_E2E_FIXTURE !== "1") notFound();
  return <Suspense><SessionActivityE2EFixture /></Suspense>;
}
