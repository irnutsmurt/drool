import { createBrowserRouter, Navigate } from "react-router";
import { AppShell } from "@/components/layout/AppShell";
import { RequireAuth, RequirePermission } from "@/auth/RequireAuth";
import { LoginPage } from "@/pages/LoginPage";
import { SetupPage } from "@/pages/SetupPage";
import { DashboardPage } from "@/pages/DashboardPage";
import { AuditPage } from "@/pages/AuditPage";
import { PlaceholderPage } from "@/pages/PlaceholderPage";
import { NotFoundPage } from "@/pages/NotFoundPage";
import { SettingsLayout } from "@/pages/settings/SettingsLayout";
import { AccountPage } from "@/pages/settings/AccountPage";
import { TokensPage } from "@/pages/settings/TokensPage";
import { UsersPage } from "@/pages/settings/UsersPage";
import { SystemPage } from "@/pages/settings/SystemPage";

export const router = createBrowserRouter([
  { path: "/login", element: <LoginPage /> },
  { path: "/setup", element: <SetupPage /> },
  {
    path: "/",
    element: (
      <RequireAuth>
        <AppShell />
      </RequireAuth>
    ),
    children: [
      { index: true, element: <DashboardPage /> },
      { path: "hosts", element: <PlaceholderPage title="Hosts" milestone="M1" /> },
      { path: "groups", element: <PlaceholderPage title="Groups" milestone="M1" /> },
      { path: "jobs", element: <PlaceholderPage title="Jobs" milestone="M2" /> },
      { path: "credentials", element: <PlaceholderPage title="Credentials" milestone="M1" /> },
      { path: "audit", element: <RequirePermission permission="audit.read"><AuditPage /></RequirePermission> },
      {
        path: "settings",
        element: <SettingsLayout />,
        children: [
          { index: true, element: <Navigate to="account" replace /> },
          { path: "account", element: <AccountPage /> },
          { path: "tokens", element: <TokensPage /> },
          { path: "users", element: <RequirePermission permission="users.manage"><UsersPage /></RequirePermission> },
          { path: "system", element: <SystemPage /> },
        ],
      },
      { path: "*", element: <NotFoundPage /> },
    ],
  },
]);
