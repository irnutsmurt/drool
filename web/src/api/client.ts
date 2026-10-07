import createClient, { type Middleware } from "openapi-fetch";
import type { components, paths } from "./schema";

export type Schemas = components["schemas"];
export type Problem = Schemas["Problem"];

let csrfToken: string | undefined;
let onUnauthenticated: (() => void) | undefined;

/** Set by the auth layer whenever /auth/me or login returns a CSRF token. */
export function setCsrfToken(token: string | undefined) {
  csrfToken = token;
}

/** The current CSRF token, for requests made outside the API client (uploads). */
export function getCsrfToken(): string | undefined {
  return csrfToken;
}

/** Called when any request returns 401 (session expired or revoked). */
export function setUnauthenticatedHandler(fn: () => void) {
  onUnauthenticated = fn;
}

const SAFE = new Set(["GET", "HEAD", "OPTIONS"]);

const middleware: Middleware = {
  onRequest({ request }) {
    if (csrfToken && !SAFE.has(request.method)) {
      request.headers.set("X-DROOL-CSRF", csrfToken);
    }
    return request;
  },
  onResponse({ request, response }) {
    if (response.status === 401 && !request.url.endsWith("/auth/login")) {
      onUnauthenticated?.();
    }
    return response;
  },
};

export const api = createClient<paths>({ baseUrl: "/api/v1", credentials: "same-origin" });
api.use(middleware);

/** Error thrown by unwrap() carrying the server's problem details. */
export class ApiError extends Error {
  constructor(
    public status: number,
    public problem: Problem | undefined,
  ) {
    super(problem?.detail ?? problem?.title ?? `Request failed (${status})`);
  }
}

/** Converts an openapi-fetch result into data or a thrown ApiError. */
export async function unwrap<T>(
  p: Promise<{ data?: T; error?: unknown; response: Response }>,
): Promise<T> {
  const { data, error, response } = await p;
  if (error !== undefined || !response.ok) {
    throw new ApiError(response.status, error as Problem | undefined);
  }
  return data as T;
}
