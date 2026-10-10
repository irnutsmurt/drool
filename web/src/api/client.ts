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

/** What a reauth_required answer says about confirming. */
export interface ReauthInfo {
  mfa: boolean;
  window_s: number;
}

let reauthHandler: ((info: ReauthInfo) => Promise<boolean>) | undefined;

/** Set by the re-authentication prompt: asked when the server answers that
 * an action needs the user to confirm who they are (M16). Resolves true when
 * confirmed; the request is then sent again. */
export function setReauthHandler(fn: ((info: ReauthInfo) => Promise<boolean>) | undefined) {
  reauthHandler = fn;
}

// Copies of state-changing requests, to repeat one after re-authentication.
const replayable = new Map<string, Request>();

const middleware: Middleware = {
  onRequest({ request, id }) {
    if (csrfToken && !SAFE.has(request.method)) {
      request.headers.set("X-DROOL-CSRF", csrfToken);
    }
    if (!SAFE.has(request.method)) replayable.set(id, request.clone());
    return request;
  },
  async onResponse({ request, response, id }) {
    const copy = replayable.get(id);
    replayable.delete(id);
    if (response.status === 401 && !request.url.endsWith("/auth/login")) {
      onUnauthenticated?.();
    }
    if (response.status === 403 && copy && reauthHandler && !request.url.endsWith("/auth/reauth")) {
      const p = (await response.clone().json().catch(() => undefined)) as (Problem & { reauth?: ReauthInfo }) | undefined;
      if (p?.code === "reauth_required" && (await reauthHandler(p.reauth ?? { mfa: false, window_s: 300 }))) {
        const headers = new Headers(copy.headers);
        if (csrfToken) headers.set("X-DROOL-CSRF", csrfToken);
        return fetch(new Request(copy, { headers }));
      }
    }
    return response;
  },
  onError({ id }) {
    replayable.delete(id);
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
