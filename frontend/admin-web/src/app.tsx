import type { ApiClient } from "@vps/shared";
import type { ReactNode } from "react";
import { Navigate, Route, Routes } from "react-router-dom";

import { Layout, LoginScreen } from "./layout.js";
import { DashboardPage, OperationsPage, TicketsPage, UsersPage } from "./pages/core.js";
import {
  AdminAuditPage,
  AdminInstanceDetailPage,
  AdminInstancesPage,
  AdminOrderDetailPage,
  AdminsPage,
  LedgerPage,
  OrdersPage,
  PaymentsPage,
  ProductsPage,
  RolesPage,
  SettingsPage,
  SubscriptionsPage,
} from "./pages/lists.js";
import { useAdminSession } from "./session-context.js";

export function App({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { session, resolving } = useAdminSession();

  if (resolving) {
    return null;
  }

  const guarded = (node: ReactNode): ReactNode =>
    session === null ? <Navigate to="/login" replace /> : node;

  return (
    <Routes>
      <Route element={<Layout />}>
        <Route path="/login" element={session === null ? <LoginScreen /> : <Navigate to="/" replace />} />
        <Route index element={guarded(<DashboardPage apiClient={apiClient} />)} />
        <Route path="/users" element={guarded(<UsersPage apiClient={apiClient} />)} />
        <Route path="/products" element={guarded(<ProductsPage apiClient={apiClient} />)} />
        <Route path="/orders" element={guarded(<OrdersPage apiClient={apiClient} />)} />
        <Route path="/orders/:orderID" element={guarded(<AdminOrderDetailPage apiClient={apiClient} />)} />
        <Route path="/payments" element={guarded(<PaymentsPage apiClient={apiClient} />)} />
        <Route path="/ledger" element={guarded(<LedgerPage apiClient={apiClient} />)} />
        <Route path="/subscriptions" element={guarded(<SubscriptionsPage apiClient={apiClient} />)} />
        <Route path="/instances" element={guarded(<AdminInstancesPage apiClient={apiClient} />)} />
        <Route path="/instances/:instanceID" element={guarded(<AdminInstanceDetailPage apiClient={apiClient} />)} />
        <Route path="/operations" element={guarded(<OperationsPage apiClient={apiClient} />)} />
        <Route path="/tickets" element={guarded(<TicketsPage apiClient={apiClient} />)} />
        <Route path="/tickets/:ticketID" element={guarded(<TicketsPage apiClient={apiClient} />)} />
        <Route path="/audit" element={guarded(<AdminAuditPage apiClient={apiClient} />)} />
        <Route path="/admins" element={guarded(<AdminsPage apiClient={apiClient} />)} />
        <Route path="/roles" element={guarded(<RolesPage apiClient={apiClient} />)} />
        <Route path="/settings" element={guarded(<SettingsPage apiClient={apiClient} />)} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}
