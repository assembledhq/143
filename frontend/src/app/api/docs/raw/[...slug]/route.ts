import { NextResponse } from "next/server";
import { getRawPublicDocBySlug } from "@/lib/docs/public-docs";

interface RawDocsRouteContext {
  params: Promise<{
    slug: string[];
  }>;
}

export async function GET(_request: Request, { params }: RawDocsRouteContext) {
  const { slug } = await params;

  try {
    const doc = getRawPublicDocBySlug(slug);
    return new NextResponse(doc.content, {
      headers: {
        "content-type": doc.contentType,
        "cache-control": "public, max-age=0, must-revalidate",
      },
    });
  } catch {
    return NextResponse.json(
      { error: { code: "not_found", message: "Public doc not found" } },
      { status: 404 }
    );
  }
}
