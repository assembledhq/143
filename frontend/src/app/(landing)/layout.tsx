import type { Metadata } from "next";

export const metadata: Metadata = {
  description: "The open-source cloud for coding agents.",
};

export default function LandingLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return <>{children}</>;
}
