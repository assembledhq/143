export default function Dashboard() {
  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-bold">Fix Queue</h1>
      <div className="rounded-lg border border-dashed border-gray-300 p-12 text-center">
        <h2 className="text-lg font-medium text-gray-900">No integrations connected</h2>
        <p className="mt-2 text-sm text-gray-500">
          Connect Sentry to start receiving issues and generating fixes.
        </p>
        <a
          href="/settings"
          className="mt-4 inline-block rounded-md bg-black px-4 py-2 text-sm text-white hover:bg-gray-800"
        >
          Connect Sentry
        </a>
      </div>
    </div>
  );
}
