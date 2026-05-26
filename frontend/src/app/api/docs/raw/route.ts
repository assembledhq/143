import { NextResponse } from "next/server";
import { getRawPublicDocBySlug } from "@/lib/docs/public-docs";

export const dynamic = "force-static";

export function GET() {
  const doc = getRawPublicDocBySlug([]);

  return new NextResponse(doc.content, {
    headers: {
      "content-type": doc.contentType,
      "cache-control": "public, max-age=0, must-revalidate",
    },
  });
}
