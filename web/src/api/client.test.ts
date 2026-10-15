import { afterEach, describe, expect, it, vi } from "vitest";
import { apiMiddleware, setCsrfToken, setReauthHandler, type ReauthInfo } from "./client";

// The re-authentication round trip in the API client: cancelling leaves the
// action undone (the server's refusal stands), confirming repeats it once,
// and a second refusal is never answered with a second prompt.

const reauthBody = {
  code: "reauth_required", status: 403, title: "Confirm it's you", type: "about:blank", detail: "Confirm it's you.",
  reauth: { mfa: false, window_s: 900, reason: "user_password", explanation: "You're setting another user's password." },
};
const refused = () => new Response(JSON.stringify(reauthBody), { status: 403, headers: { "Content-Type": "application/problem+json" } });
const ok = () => new Response(JSON.stringify({ id: "usr_1" }), { status: 200, headers: { "Content-Type": "application/json" } });

async function send(id: string) {
  const request = new Request("https://drool.example.net/api/v1/users/usr_1", { method: "PATCH", body: JSON.stringify({ password: "a-new-password-12" }) });
  const sent = (await apiMiddleware.onRequest!({ request, id, schemaPath: "/users/{id}", params: {}, options: {} as never })) as Request;
  return { sent, respond: (response: Response) => apiMiddleware.onResponse!({ request: sent, response, id, schemaPath: "/users/{id}", params: {}, options: {} as never }) };
}

afterEach(() => {
  vi.unstubAllGlobals();
  setReauthHandler(undefined);
});

describe("re-authentication round trip", () => {
  it("cancelling leaves the refusal in place and sends nothing again", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const asked: ReauthInfo[] = [];
    setReauthHandler(async (i) => { asked.push(i); return false; });
    const { respond } = await send("cancel");
    const res = (await respond(refused())) as Response;
    expect(res.status).toBe(403);
    expect(fetchMock).not.toHaveBeenCalled();
    expect(asked).toHaveLength(1);
    expect(asked[0]!.reason).toBe("user_password");
    expect(asked[0]!.explanation).toContain("another user's password");
  });

  it("confirming repeats the request exactly once, with the current CSRF token and the same body", async () => {
    setCsrfToken("csrf-after-confirming");
    const fetchMock = vi.fn(async (r: Request) => { void r; return ok(); });
    vi.stubGlobal("fetch", fetchMock);
    setReauthHandler(async () => true);
    const { respond } = await send("confirm");
    const res = (await respond(refused())) as Response;
    expect(res.status).toBe(200);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const again = fetchMock.mock.calls[0]![0];
    expect(again.method).toBe("PATCH");
    expect(again.headers.get("X-DROOL-CSRF")).toBe("csrf-after-confirming");
    expect(await again.text()).toContain("a-new-password-12");
  });

  it("a second refusal is returned as it is: one prompt, one repeat, no loop", async () => {
    const fetchMock = vi.fn(async () => refused());
    vi.stubGlobal("fetch", fetchMock);
    let prompts = 0;
    setReauthHandler(async () => { prompts++; return true; });
    const { respond } = await send("twice");
    const res = (await respond(refused())) as Response;
    expect(res.status).toBe(403);
    expect(prompts).toBe(1);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("other refusals never ask", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    let prompts = 0;
    setReauthHandler(async () => { prompts++; return true; });
    const { respond } = await send("forbidden");
    const res = (await respond(new Response(JSON.stringify({ code: "forbidden", status: 403 }), { status: 403 }))) as Response;
    expect(res.status).toBe(403);
    expect(prompts).toBe(0);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
