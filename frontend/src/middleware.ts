import { NextResponse } from "next/server";
import type { NextRequest } from "next/server";

export function middleware(request: NextRequest) {
  const sessionToken = request.cookies.get("session_token");

  // If user has a session cookie and is visiting /login, redirect to /sessions.
  // The cookie is not validated here — if it's expired or invalid, the dashboard's
  // auth check will catch it and redirect back to /login.
  if (sessionToken?.value) {
    return NextResponse.redirect(new URL("/sessions", request.url));
  }

  return NextResponse.next();
}

export const config = {
  matcher: ["/", "/login"],
};
