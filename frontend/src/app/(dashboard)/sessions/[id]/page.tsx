export default async function SessionDetailPage({ params }: { params: Promise<{ id: string }> }) {
  // App Router requires a page.tsx here for [id] segment matching; the persistent
  // SessionsLayout shell owns the actual detail content. Awaiting params satisfies
  // the async Server Component contract without doing anything with the value.
  await params;
  return null;
}
