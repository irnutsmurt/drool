import { lazy, Suspense, type ReactNode } from "react";
import { createBrowserRouter, Navigate } from "react-router";
import { AppShell } from "@/components/layout/AppShell";
import { RequireAuth, RequirePermission } from "@/auth/RequireAuth";
import { LoginPage } from "@/pages/LoginPage";
import { SetupPage } from "@/pages/SetupPage";
import { DashboardPage } from "@/pages/DashboardPage";
import { AuditPage } from "@/pages/AuditPage";
import { NotFoundPage } from "@/pages/NotFoundPage";
import { Loading } from "@/components/ui/States";
import { HostsPage } from "@/pages/hosts/HostsPage";
import { AddHostPage } from "@/pages/hosts/AddHostPage";
import { HostLayout } from "@/components/hosts/workspace/HostLayout";
import { HostOverviewPage } from "@/pages/hosts/HostOverviewPage";
import { HostManagementPage } from "@/pages/hosts/HostManagementPage";
import { HostActivityPage } from "@/pages/hosts/HostActivityPage";
import { GroupsPage } from "@/pages/GroupsPage";
import { PackPage } from "@/pages/PackPage";
import { CredentialsPage } from "@/pages/CredentialsPage";
import { JobsPage } from "@/pages/jobs/JobsPage";
import { NewJobPage } from "@/pages/jobs/NewJobPage";
import { JobDetailPage } from "@/pages/jobs/JobDetailPage";
import { SettingsLayout } from "@/pages/settings/SettingsLayout";
import { AccountPage } from "@/pages/settings/AccountPage";
import { TokensPage } from "@/pages/settings/TokensPage";
import { UsersPage } from "@/pages/settings/UsersPage";
import { SystemPage } from "@/pages/settings/SystemPage";
import { SettingsDirectoryPage } from "@/pages/settings/SettingsDirectoryPage";

// Larger, less used pages load on demand to keep the main bundle small.
const FilesPage = lazy(() => import("@/pages/files/FilesPage").then((x) => ({ default: x.FilesPage })));
const EditFilePage = lazy(() => import("@/pages/files/EditFilePage").then((x) => ({ default: x.EditFilePage })));
const FileDeploymentPage = lazy(() => import("@/pages/files/FileDeploymentPage").then((x) => ({ default: x.FileDeploymentPage })));
const AgentsPage = lazy(() => import("@/pages/agents/AgentsPage").then((x) => ({ default: x.AgentsPage })));
const FirewallPage = lazy(() => import("@/pages/firewall/FirewallPage").then((x) => ({ default: x.FirewallPage })));
const NetworkPage = lazy(() => import("@/pages/network/NetworkPage").then((x) => ({ default: x.NetworkPage })));
const MonitoringPage = lazy(() => import("@/pages/monitoring/MonitoringPage").then((x) => ({ default: x.MonitoringPage })));
const HostMonitoringPage = lazy(() => import("@/pages/monitoring/HostMonitoringPage").then((x) => ({ default: x.HostMonitoringPage })));
const AlertsPage = lazy(() => import("@/pages/alerts/AlertsPage").then((x) => ({ default: x.AlertsPage })));
const AlertDetailPage = lazy(() => import("@/pages/alerts/AlertDetailPage").then((x) => ({ default: x.AlertDetailPage })));
const NotificationsPage = lazy(() => import("@/pages/settings/NotificationsPage").then((x) => ({ default: x.NotificationsPage })));
const MonitoringSettingsPage = lazy(() => import("@/pages/monitoring/MonitoringSettingsPage").then((x) => ({ default: x.MonitoringSettingsPage })));
const SchedulesPage = lazy(() => import("@/pages/automation/SchedulesPage").then((x) => ({ default: x.SchedulesPage })));
const ScheduleEditorPage = lazy(() => import("@/pages/automation/ScheduleEditorPage").then((x) => ({ default: x.ScheduleEditorPage })));
const ScheduleDetailPage = lazy(() => import("@/pages/automation/ScheduleDetailPage").then((x) => ({ default: x.ScheduleDetailPage })));
const WorkflowsPage = lazy(() => import("@/pages/automation/WorkflowsPage").then((x) => ({ default: x.WorkflowsPage })));
const WorkflowEditorPage = lazy(() => import("@/pages/automation/WorkflowEditorPage").then((x) => ({ default: x.WorkflowEditorPage })));
const DesiredOverviewPage = lazy(() => import("@/pages/desired/DesiredOverviewPage").then((x) => ({ default: x.DesiredOverviewPage })));
const BaselinesPage = lazy(() => import("@/pages/desired/BaselinesPage").then((x) => ({ default: x.BaselinesPage })));
const BaselineDetailPage = lazy(() => import("@/pages/desired/BaselineDetailPage").then((x) => ({ default: x.BaselineDetailPage })));
const BaselineEditorPage = lazy(() => import("@/pages/desired/BaselineEditorPage").then((x) => ({ default: x.BaselineEditorPage })));
const DesiredHistoryPage = lazy(() => import("@/pages/desired/DesiredHistoryPage").then((x) => ({ default: x.DesiredHistoryPage })));
const HostDesiredPage = lazy(() => import("@/pages/desired/HostDesiredPage").then((x) => ({ default: x.HostDesiredPage })));
const HostFirewallPage = lazy(() => import("@/pages/firewall/HostFirewallPage").then((x) => ({ default: x.HostFirewallPage })));
const HostNetworkPage = lazy(() => import("@/pages/network/HostNetworkPage").then((x) => ({ default: x.HostNetworkPage })));
const later = (el: ReactNode) => <Suspense fallback={<Loading />}>{el}</Suspense>;

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
      // A host's workspace: one header and local navigation for every page
      // about the host. The addresses are the ones these pages always had.
      {
        path: "hosts/:id",
        element: <RequirePermission permission="hosts.read"><HostLayout /></RequirePermission>,
        children: [
          { index: true, element: <HostOverviewPage /> },
          { path: "monitoring", element: <RequirePermission permission="monitoring.read">{later(<HostMonitoringPage />)}</RequirePermission> },
          { path: "files", element: <RequirePermission permission="files.read">{later(<FilesPage />)}</RequirePermission> },
          { path: "files/edit", element: <RequirePermission permission="files.read">{later(<EditFilePage />)}</RequirePermission> },
          { path: "network", element: <RequirePermission permission="network.read">{later(<HostNetworkPage />)}</RequirePermission> },
          { path: "firewall", element: <RequirePermission permission="firewall.read">{later(<HostFirewallPage />)}</RequirePermission> },
          { path: "desired", element: <RequirePermission permission="desired.read">{later(<HostDesiredPage />)}</RequirePermission> },
          { path: "management", element: <HostManagementPage /> },
          { path: "activity", element: <RequirePermission permission="jobs.read"><HostActivityPage /></RequirePermission> },
        ],
      },
      { path: "monitoring", element: <RequirePermission permission="monitoring.read">{later(<MonitoringPage />)}</RequirePermission> },
      { path: "monitoring/settings", element: <RequirePermission permission="monitoring.read">{later(<MonitoringSettingsPage />)}</RequirePermission> },
      { path: "alerts", element: <RequirePermission permission="alerts.read">{later(<AlertsPage />)}</RequirePermission> },
      { path: "alerts/:id", element: <RequirePermission permission="alerts.read">{later(<AlertDetailPage />)}</RequirePermission> },
      { path: "file-deployments/:id", element: <RequirePermission permission="files.read">{later(<FileDeploymentPage />)}</RequirePermission> },
      { path: "groups", element: <RequirePermission permission="hosts.read"><GroupsPage /></RequirePermission> },
      { path: "groups/:id", element: <RequirePermission permission="hosts.read"><PackPage /></RequirePermission> },
      { path: "jobs", element: <RequirePermission permission="jobs.read"><JobsPage /></RequirePermission> },
      { path: "jobs/new", element: <RequirePermission permission="jobs.execute"><NewJobPage /></RequirePermission> },
      { path: "jobs/:id", element: <RequirePermission permission="jobs.read"><JobDetailPage /></RequirePermission> },
      { path: "jobs/schedules", element: <RequirePermission permission="schedules.read">{later(<SchedulesPage />)}</RequirePermission> },
      { path: "jobs/schedules/new", element: <RequirePermission permission="schedules.manage">{later(<ScheduleEditorPage />)}</RequirePermission> },
      { path: "jobs/schedules/:id", element: <RequirePermission permission="schedules.read">{later(<ScheduleDetailPage />)}</RequirePermission> },
      { path: "jobs/schedules/:id/edit", element: <RequirePermission permission="schedules.manage">{later(<ScheduleEditorPage />)}</RequirePermission> },
      { path: "jobs/workflows", element: <RequirePermission permission="workflows.read">{later(<WorkflowsPage />)}</RequirePermission> },
      { path: "jobs/workflows/new", element: <RequirePermission permission="workflows.manage">{later(<WorkflowEditorPage />)}</RequirePermission> },
      { path: "jobs/workflows/:id", element: <RequirePermission permission="workflows.read">{later(<WorkflowEditorPage />)}</RequirePermission> },
      { path: "desired", element: <RequirePermission permission="desired.read">{later(<DesiredOverviewPage />)}</RequirePermission> },
      { path: "desired/baselines", element: <RequirePermission permission="desired.read">{later(<BaselinesPage />)}</RequirePermission> },
      { path: "desired/baselines/new", element: <RequirePermission permission="desired.manage">{later(<BaselineEditorPage />)}</RequirePermission> },
      { path: "desired/baselines/:id", element: <RequirePermission permission="desired.read">{later(<BaselineDetailPage />)}</RequirePermission> },
      { path: "desired/baselines/:id/edit", element: <RequirePermission permission="desired.manage">{later(<BaselineEditorPage key="edit" />)}</RequirePermission> },
      { path: "desired/history", element: <RequirePermission permission="desired.read">{later(<DesiredHistoryPage />)}</RequirePermission> },
      { path: "agents", element: <RequirePermission permission="agents.read">{later(<AgentsPage />)}</RequirePermission> },
      { path: "firewall", element: <RequirePermission permission="firewall.read">{later(<FirewallPage />)}</RequirePermission> },
      { path: "network", element: <RequirePermission permission="network.read">{later(<NetworkPage />)}</RequirePermission> },
      { path: "credentials", element: <RequirePermission permission="credentials.use"><CredentialsPage /></RequirePermission> },
      { path: "audit", element: <RequirePermission permission="audit.read"><AuditPage /></RequirePermission> },
      {
        path: "settings",
        element: <SettingsLayout />,
        children: [
          { index: true, element: <SettingsDirectoryPage /> },
          { path: "account", element: <AccountPage /> },
          { path: "tokens", element: <TokensPage /> },
          { path: "users", element: <RequirePermission permission="users.manage"><UsersPage /></RequirePermission> },
          // Monitoring settings live under Monitoring now; old links still work.
          { path: "monitoring", element: <Navigate to="/monitoring/settings" replace /> },
          { path: "notifications", element: <RequirePermission permission="notifications.read">{later(<NotificationsPage />)}</RequirePermission> },
          { path: "system", element: <SystemPage /> },
        ],
      },
      { path: "*", element: <NotFoundPage /> },
    ],
  },
]);
