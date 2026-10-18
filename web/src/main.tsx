import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "react-router";
import { AuthProvider } from "@/auth/AuthContext";
import { ReauthPrompt } from "@/auth/ReauthPrompt";
import { ApiError } from "@/api/client";
import { watchFocusLoss } from "@/components/ui/modal";
import { router } from "@/router";
import "@/styles/index.css";

// Focus never falls back to the document when what had it disappears or is
// disabled under it — on every page, sign-in and setup included.
watchFocusLoss();

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Don't retry client errors (401/403/404/422); retry transient failures once.
      retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 1,
      refetchOnWindowFocus: false,
    },
  },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <ReauthPrompt />
        <RouterProvider router={router} />
      </AuthProvider>
    </QueryClientProvider>
  </StrictMode>,
);
