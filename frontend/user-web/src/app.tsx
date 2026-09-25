/**
 * Application shell and routes (docs/10's page map).
 *
 * Signed-out visitors see the catalog, the auth screens and nothing else:
 * the guarded routes redirect to sign-in, and the signed-in routes render
 * inside the shell once the session has been recovered.
 */

import type { ApiClient } from "@vps/shared";
import { useTranslation } from "react-i18next";
import type { ReactNode } from "react";
import { Navigate, Route, Routes } from "react-router-dom";

import { Layout } from "./layout.js";
import { CatalogPage, CheckoutPage, OrderDetailPage, OrdersPage } from "./pages/commerce.js";
import { DashboardPage, InvoicesPage, NotificationsPage, WalletPage } from "./pages/billing.js";
import { InstanceDetailPage, InstancesPage } from "./pages/instances.js";
import { AccountPage, TicketDetailPage, TicketsPage } from "./pages/support.js";
import { LoginPage, RegisterPage } from "./pages/auth.js";
import { useSession } from "./session-context.js";

export function App({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const { session, resolving } = useSession();

  if (resolving) {
    return (
      <div className="flex min-h-full items-center justify-center">
        <p className="text-sm text-content-muted">{t("common.state.loading")}</p>
      </div>
    );
  }

  const guarded = (node: ReactNode): ReactNode =>
    session === null ? <Navigate to="/login" replace /> : node;

  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={session === null ? <Navigate to="/catalog" replace /> : <DashboardPage apiClient={apiClient} />} />
        <Route path="/catalog" element={<CatalogPage apiClient={apiClient} />} />
        <Route path="/login" element={session === null ? <LoginPage /> : <Navigate to="/" replace />} />
        <Route path="/register" element={<RegisterPage apiClient={apiClient} />} />
        <Route path="/checkout" element={guarded(<CheckoutPage apiClient={apiClient} />)} />
        <Route path="/instances" element={guarded(<InstancesPage apiClient={apiClient} />)} />
        <Route path="/instances/:instanceID" element={guarded(<InstanceDetailPage apiClient={apiClient} />)} />
        <Route path="/orders" element={guarded(<OrdersPage apiClient={apiClient} />)} />
        <Route path="/orders/:orderID" element={guarded(<OrderDetailPage apiClient={apiClient} />)} />
        <Route path="/invoices" element={guarded(<InvoicesPage apiClient={apiClient} />)} />
        <Route path="/wallet" element={guarded(<WalletPage apiClient={apiClient} />)} />
        <Route path="/notifications" element={guarded(<NotificationsPage apiClient={apiClient} />)} />
        <Route path="/tickets" element={guarded(<TicketsPage apiClient={apiClient} />)} />
        <Route path="/tickets/:ticketID" element={guarded(<TicketDetailPage apiClient={apiClient} />)} />
        <Route path="/account" element={guarded(<AccountPage />)} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}
