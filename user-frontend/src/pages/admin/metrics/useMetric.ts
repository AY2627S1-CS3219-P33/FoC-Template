// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend admin page and useMetric behavior.
// Author review: I validated correctness, edited for style, and added boundary checks.

import { useCallback, useEffect, useRef, useState } from "react";
import { AdminError } from "../../../api/admin";

// Raised when the access token could not be obtained at all, which is a
// different problem from the service refusing or failing a request. It keeps
// Auth0's short error code (never the raw response) so the page can say why.
class TokenError extends Error {
  readonly auth0Code: string | undefined;
  readonly auth0Description: string | undefined;

  constructor(auth0Code?: string, auth0Description?: string) {
    super("Could not get an access token for the admin service.");
    this.auth0Code = auth0Code;
    this.auth0Description = auth0Description;
  }
}

// Auth0's own explanation, shortened and stripped of control characters.
function auth0DescriptionOf(err: unknown): string | undefined {
  const description = (err as { description?: unknown } | null)?.description;
  if (typeof description !== "string") return undefined;
  const clean = description.replace(/[\u0000-\u001f\u007f]/g, " ").trim().slice(0, 240);
  return clean === "" ? undefined : clean;
}

// Auth0's error codes are short snake_case words; anything else is ignored.
function auth0CodeOf(err: unknown): string | undefined {
  const code = (err as { code?: unknown } | null)?.code;
  return typeof code === "string" && /^[a-z_]{1,40}$/i.test(code) ? code : undefined;
}

// What each code from Auth0's silent token request means for this API, and
// what to change. The silent request cannot show a screen, so anything that
// would need one fails with a code like these.
function tokenMessage(code: string | undefined, description?: string): string {
  const base = "Could not get an access token for the admin service.";
  switch (code) {
    case "consent_required":
      return `${base} Auth0 wants consent for this API and a silent request cannot ask. In Auth0, open the admin-service API's settings and turn on “Allow Skipping User Consent”. (Auth0 said: consent_required)`;
    case "login_required":
    case "interaction_required":
      return `${base} Auth0 could not reuse your login silently. When this site and Auth0 are on different domains (for example through ngrok), browsers often block the cookie that needs. Allow third-party cookies for your Auth0 domain, or sign out and back in. (Auth0 said: ${code})`;
    case "access_denied":
    case "unauthorized_client":
      return `${base} Auth0 refused: this application is not allowed to get tokens for the admin-service API, or the API does not exist with exactly the audience https://api.foc.local/admin-service. (Auth0 said: ${code})`;
    case "timeout":
      return `${base} Auth0 did not answer in time. Check that this site's address is in the application's Allowed Web Origins in Auth0. (Auth0 said: timeout)`;
    case undefined:
      return `${base} Its Auth0 API or your dashboard:read permission may not be set up yet.`;
    default:
      return `${base} Auth0 refused the request. (Auth0 said: ${code}${description ? `: ${description}` : ""})`;
  }
}

export function adminError(err: unknown): string {
  if (err instanceof TokenError) {
    return tokenMessage(err.auth0Code, err.auth0Description);
  }
  if (!(err instanceof AdminError)) {
    return "Could not reach the admin service.";
  }
  switch (err.code) {
    case "UNAUTHENTICATED":
      return "Please sign in again to continue.";
    case "FORBIDDEN":
      return "Your account does not have the dashboard:read permission.";
    case "INVALID_ARGUMENT":
      return err.message;
    case "DEPENDENCY_UNAVAILABLE":
      return "The admin service cannot verify your sign-in right now. Try again shortly.";
    default:
      return "The admin service had an unexpected problem.";
  }
}

export interface Metric<T> {
  data: T | undefined;
  error: string | null;
  loading: boolean;
  reload: () => void;
}

// Loads one panel's data. Each panel owns its own error: a failure shows in
// that panel, and the page around it keeps working. A reload keeps the
// previous data in view (the panel dims it) instead of blanking the panel,
// and a response that arrives after a newer request was made is dropped.
export function useMetric<T>(
  getToken: () => Promise<string>,
  fetcher: (token: string) => Promise<T>,
  // Anything the fetcher closes over, so a change reloads.
  deps: readonly unknown[],
): Metric<T> {
  const [data, setData] = useState<T | undefined>();
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const latest = useRef(0);
  // The fetcher is a new function on every render; the deps decide when to run.
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;

  const load = useCallback(async () => {
    const request = ++latest.current;
    setLoading(true);
    setError(null);
    try {
      let token: string;
      try {
        token = await getToken();
      } catch (tokenErr) {
        // The code is also in the console, for whoever is debugging.
        console.error("admin-service token request failed:", auth0CodeOf(tokenErr) ?? tokenErr, auth0DescriptionOf(tokenErr) ?? "");
        throw new TokenError(auth0CodeOf(tokenErr), auth0DescriptionOf(tokenErr));
      }
      const result = await fetcherRef.current(token);
      if (request === latest.current) setData(result);
    } catch (err) {
      if (request === latest.current) setError(adminError(err));
    } finally {
      if (request === latest.current) setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [getToken, ...deps]);

  useEffect(() => {
    void load();
  }, [load]);

  return { data, error, loading, reload: () => void load() };
}
