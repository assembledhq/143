export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export function GET() {
  return new Response("ok", {
    status: 200,
    headers: { "content-type": "text/plain; charset=utf-8" },
  });
}
