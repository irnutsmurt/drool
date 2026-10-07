import { lazy, Suspense, type ReactNode } from "react";
import { createBrowserRouter, Navigate } from "react-router";
import { AppShell } from "@/components/layout/AppShell";
import { RequireAuth, RequirePermission } from "@/auth/RequireAuth";
import { LoginPage } from "@/pages/LoginPage";
import { SetupPage } from "@/pages/SetupPage";
import { DashboardPage } from "@/pages/DashboardPage";
import { AuditPage } from "@/pages/AuditPage";
import { NotFoundPage } from "@/pages/NotFoundPage";
import { HostsPage } from "@/pages/hosts/HostsPage";
import { AddHostPage } from "@/pages/hosts/AddHostPage";
import { HostDetailPage } from "@/pages/hosts/HostDetailPage";
import { GroupsPage } from "@/pages/GroupsPage";
import { CredentialsPage } from "@/pages/CredentialsPage";
import { JobsPage } from "@/pages/jobs/JobsPage";
import { NewJobPage } from "@/pages/jobs/NewJobPage";
import { JobDetailPage } from "@/pages/jobs/JobDetailPage";
import { SettingsLayout } from "@/pages/settings/SettingsLayout";
import { AccountPage } from "@/pages/settings/AccountPage";
import { TokensPage } from "@/pages/settings/TokensPage";
import { UsersPage } from "@/pages/settings/UsersPage";
import { SystemPage } from "@/pages/settings/SystemPage";

// Larger, less used pages load on demand to keep the main bundle small.
const FilesPage = lazy(() => import("@/pages/files/FilesPage").then((x) => ({ default: x.FilesPage })));
const EditFilePage = lazy(() => import("@/pages/files/EditFilePage").then((x) => ({ default: x.EditFilePage })));
const FileDeploymentPage = lazy(() => import("@/pages/files/FileDeploymentPage").then((x) => ({ default: x.FileDeploymentPage })));
const AgentsPage = lazy(() => import("@/pages/agents/AgentsPage").then((x) => ({ default: x.AgentsPage })));
const FirewallPage = lazy(() => import("@/pages/firewall/FirewallPage").then((x) => ({ default: x.FirewallPage })));
const later = (el: ReactNode) => <Suspense fallback={null}>{el}</Suspense>;

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
      { path: "hosts", element: <RequirePermission permission="hosts.read"><HostsPage /></RequirePermission> },
      { path: "hosts/new", element: <RequirePermission permission="hosts.write"><AddHostPage /></RequirePermission> },
      { path: "hosts/:id", element: <RequirePermission permission="hosts.read"><HostDetailPage /></RequirePermission> },
      { path: "hosts/:id/files", element: <RequirePermission permission="files.read">{later(<FilesPage />)}</RequirePermission> },
      { path: "hosts/:id/files/edit", element: <RequirePermission permission="files.read">{later(<EditFilePage />)}</RequirePermission> },
      { path: "file-deployments/:id", element: <RequirePermission permission="files.read">{later(<FileDeploymentPage />)}</RequirePermission> },
      { path: "groups", element: <RequirePermission permission="hosts.read"><GroupsPage /></RequirePermission> },
      { path: "jobs", element: <RequirePermission permission="jobs.read"><JobsPage /></RequirePermission> },
      { path: "jobs/new", element: <RequirePermission permission="jobs.execute"><NewJobPage /></RequirePermission> },
      { path: "jobs/:id", element: <RequirePermission permission="jobs.read"><JobDetailPage /></RequirePermission> },
      { path: "agents", element: <RequirePermission permission="agents.read">{later(<AgentsPage />)}</RequirePermission> },
      { path: "firewall", element: <RequirePermission permission="firewall.read">{later(<FirewallPage />)}</RequirePermission> },
      { path: "credentials", element: <RequirePermission permission="credentials.use"><CredentialsPage /></RequirePermission> },
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
