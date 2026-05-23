import { NextResponse } from "next/server";
import { source } from "@/lib/source";
import { getPublicDocsLlmsText } from "@/lib/docs/public-docs";

export const revalidate = false;

export function GET() {
  const pages = source
    .getPages()
    .sort((a, b) => a.data.order - b.data.order || a.url.localeCompare(b.url))
    .map((page) => ({
      title: page.data.title,
      url: page.url,
      llmSummary: page.data.llm_summary,
    }));

  return new NextResponse(getPublicDocsLlmsText(pages), {
    headers: {
      "content-type": "text/plain; charset=utf-8",
      "cache-control": "public, max-age=0, must-revalidate",
    },
  });
}
