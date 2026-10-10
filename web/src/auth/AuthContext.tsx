import { createContext, useContext, useEffect, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, setCsrfToken, setUnauthenticatedHandler, unwrap, type Schemas } from "@/api/client";

type Me = Schemas["Me"];

/** The password was right; the second factor is next (M16). */
export interface MfaChallenge {
  challenge: string;
}

interface AuthState {
  me: Me | null;
  loading: boolean;
  can: (permission: string) => boolean;
  /** Signs in, or returns the challenge when the account has two-factor
   * authentication: finish with completeLogin. */
  login: (username: string, password: string) => Promise<MfaChallenge | undefined>;
  completeLogin: (challenge: string, second: { code?: string; recovery_code?: string }) => Promise<void>;
  /** First-admin setup: creates the admin and signs them in. */
  claim: (code: string, username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);
export const meQueryKey = ["auth", "me"] as const;
export const setupStatusQueryKey = ["auth", "setup"] as const;

/** Whether the controller still needs its first administrator. */
export function useSetupStatus() {
  return useQuery({
    queryKey: setupStatusQueryKey,
    queryFn: () => unwrap(api.GET("/setup/status")),
    staleTime: 30_000,
  });
}

async function fetchMe(): Promise<Me | null> {
  try {
    const me = await unwrap(api.GET("/auth/me"));
    // Set the CSRF token before any page renders: child components' effects
    // (and their first requests) run before this provider's effects would.
    setCsrfToken(me.csrf_token);
    return me;
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) return null;
    throw e;
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  const meQuery = useQuery({ queryKey: meQueryKey, queryFn: fetchMe, staleTime: 60_000, retry: false });
  const me = meQuery.data ?? null;

  useEffect(() => {
    setCsrfToken(me?.csrf_token);
  }, [me]);

  useEffect(() => {
    setUnauthenticatedHandler(() => {
      if (qc.getQueryData(meQueryKey)) {
        qc.setQueryData(meQueryKey, null);
        qc.removeQueries({ predicate: (q) => q.queryKey[0] !== "auth" });
      }
    });
  }, [qc]);

  const signedIn = (data: Me) => {
    setCsrfToken(data.csrf_token);
    qc.setQueryData(meQueryKey, data);
  };
  const loginMut = useMutation({
    mutationFn: (v: { username: string; password: string }) => unwrap(api.POST("/auth/login", { body: v })),
    onSuccess: (data) => {
      if (!("mfa_required" in data)) signedIn(data);
    },
  });
  const mfaMut = useMutation({
    mutationFn: (v: { challenge: string; code?: string; recovery_code?: string }) => unwrap(api.POST("/auth/login/mfa", { body: v })),
    onSuccess: signedIn,
  });

  const claimMut = useMutation({
    mutationFn: (v: { code: string; username: string; password: string }) =>
      unwrap(api.POST("/setup/claim", { body: v })),
    onSuccess: (data) => {
      setCsrfToken(data.csrf_token);
      qc.setQueryData(meQueryKey, data);
      qc.setQueryData(setupStatusQueryKey, { setup_required: false, code_available: false });
    },
  });

  const logoutMut = useMutation({
    mutationFn: () => unwrap(api.POST("/auth/logout")),
    onSettled: () => {
      setCsrfToken(undefined);
      qc.setQueryData(meQueryKey, null);
      qc.removeQueries({ predicate: (q) => q.queryKey[0] !== "auth" });
    },
  });

  const value: AuthState = {
    me,
    loading: meQuery.isPending,
    can: (p) => !!me?.permissions.includes(p),
    login: async (username, password) => {
      const data = await loginMut.mutateAsync({ username, password });
      return "mfa_required" in data ? { challenge: data.challenge } : undefined;
    },
    completeLogin: async (challenge, second) => {
      await mfaMut.mutateAsync({ challenge, ...second });
    },
    claim: async (code, username, password) => {
      await claimMut.mutateAsync({ code, username, password });
    },
    logout: async () => {
      await logoutMut.mutateAsync().catch(() => undefined);
    },
  };
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth outside AuthProvider");
  return ctx;
}
