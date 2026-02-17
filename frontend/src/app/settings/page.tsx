"use client";

import { api } from "@/lib/api";

export default function SettingsPage() {
  return (
    <div className="space-y-8">
      <h1 className="text-2xl font-bold">Settings</h1>

      <section className="space-y-4">
        <h2 className="text-lg font-semibold">General</h2>
        <div className="rounded-lg border p-6 space-y-4">
          <div>
            <label className="block text-sm font-medium text-gray-700">
              Organization Name
            </label>
            <input
              type="text"
              placeholder="My Organization"
              className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus:border-black focus:outline-none focus:ring-1 focus:ring-black"
            />
          </div>
        </div>
      </section>

      <section className="space-y-4">
        <h2 className="text-lg font-semibold">Integrations</h2>
        <div className="rounded-lg border p-6">
          <div className="flex items-center justify-between">
            <div>
              <h3 className="font-medium">GitHub</h3>
              <p className="text-sm text-gray-500">
                Connect your GitHub account to sync repositories.
              </p>
            </div>
            <button
              onClick={() => api.auth.login()}
              className="rounded-md bg-black px-4 py-2 text-sm text-white hover:bg-gray-800"
            >
              Connect GitHub
            </button>
          </div>
        </div>
      </section>
    </div>
  );
}
