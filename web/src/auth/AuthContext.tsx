import { createContext, useContext, useEffect, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, setCsrfToken, setUnauthenticatedHandler, unwrap, type Schemas } from "@/api/client";

type Me = Schemas["Me"];

interface AuthState {
  me: Me | null;
  loading: boolean;
  can: (permission: string) => boolean;
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);
export const meQueryKey = ["auth", "me"] as const;

async function fetchMe(): Promise<Me | null> {
  try {
    return await unwrap(api.GET("/auth/me"));
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

  const loginMut = useMutation({
    mutationFn: (v: { username: string; password: string }) => unwrap(api.POST("/auth/login", { body: v })),
    onSuccess: (data) => {
      setCsrfToken(data.csrf_token);
      qc.setQueryData(meQueryKey, data);
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
      await loginMut.mutateAsync({ username, password });
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
