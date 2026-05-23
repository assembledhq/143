import { NextResponse } from "next/server";
import { getPublicDocsLlmsText } from "@/lib/docs/public-docs";

export const revalidate = false;

export function GET() {
  return new NextResponse(getPublicDocsLlmsText(), {
    headers: {
      "content-type": "text/plain; charset=utf-8",
      "cache-control": "public, max-age=0, must-revalidate",
    },
  });
}
