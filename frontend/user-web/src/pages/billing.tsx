/**
 * The billing screens: dashboard, invoices, wallet, notifications.
 *
 * The dashboard answers docs/10's five-second questions from the same
 * queries the other pages use; the wallet shows the projection as the
 * balance and the ledger as the movements behind it.
 */

import { Button, Card, ErrorState, LoadingState, StatusBadge, type ApiClient } from "@vps/shared";
import { useQueryClient } from "@tanstack/react-query";
import { useMutation, useQuery } from "../hooks.js";
import { useTranslation } from "react-i18next";
import type { ReactNode } from "react";
import { Link } from "react-router-dom";

import { getWallet, listInvoices, listInstances, listNotifications, listSubscriptions, markNotificationRead } from "../api/endpoints.js";
import type { InvoiceDetail } from "../api/types.js";
import { useDate, useMoney } from "../format-hooks.js";
import { Page, Stat, } from "../page-fragments.js";
import { useSession } from "../session-context.js";

export function DashboardPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const money = useMoney();
  const date = useDate();
  const instances = useQuery({ queryKey: ["/api/v1/instances"], queryFn: () => listInstances(apiClient), refetchInterval: 15_000 });
  const subscriptions = useQuery({ queryKey: ["/api/v1/subscriptions"], queryFn: () => listSubscriptions(apiClient) });
  const wallet = useQuery({ queryKey: ["/api/v1/wallet"], queryFn: () => getWallet(apiClient) });

  if (instances.isPending || wallet.isPending) {
    return <LoadingState />;
  }
  if (instances.isError || wallet.isError) {
    const error = instances.isError ? instances.error : wallet.error;
    return <ErrorState messageKey={error?.messageKey ?? "errors.unknown"} {...(error?.requestId === undefined ? {} : { requestId: error.requestId })} />;
  }

  const data = instances.data;
  if (data.length === 0) {
    return (
      <Page title={t("user.dashboard.title")} subtitle={t("user.dashboard.subtitle")}>
        <Card>
          <p className="text-sm font-medium text-content">{t("user.dashboard.empty.title")}</p>
          <p className="text-xs text-content-muted">{t("user.dashboard.empty.description")}</p>
          <Link to="/catalog" className="text-sm underline text-content">
            {t("user.dashboard.empty.action")}
          </Link>
        </Card>
      </Page>
    );
  }

  const running = data.filter((i) => i.observed_state === "running").length;
  const attention = data.filter((i) => i.observed_state === "error" || i.desired_state !== i.observed_state && i.observed_state !== "running").length;
  const balance = wallet.data.wallets.reduce((sum, w) => sum + w.available_balance_minor, 0);
  const currency = wallet.data.wallets[0]?.currency ?? "CNY";
  // eslint-disable-next-line react-hooks/purity -- the "expiring" window is inherently relative to now
  const sevenDaysAhead = Date.now() + 7 * 24 * 60 * 60 * 1000;
  const expiring = (subscriptions.data ?? []).filter((s) => {
    if (s.current_period_end === null) return false;
    return new Date(s.current_period_end).getTime() <= sevenDaysAhead;
  }).length;
  // The instance payload's traffic rows are the recorded periods; the current
  // one is the newest. The limit is not on this payload, so the stat shows
  // usage rather than inventing a ratio.
  const trafficBytes = data.reduce((sum, i) => {
    const latest = i.traffic[0];
    return sum + (latest ? latest.rx_bytes + latest.tx_bytes : 0);
  }, 0);

  return (
    <Page title={t("user.dashboard.title")} subtitle={t("user.dashboard.subtitle")}>
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <Stat label={t("user.dashboard.stats.instances")} value={data.length} />
        <Stat label={t("user.dashboard.stats.running")} value={running} />
        <Stat label={t("user.dashboard.stats.attention")} value={attention} />
        <Stat label={t("user.dashboard.stats.balance")} value={money(balance, currency)} />
        <Stat label={t("user.dashboard.stats.expiring")} value={expiring} />
        <Stat label={t("user.dashboard.stats.traffic")} value={(trafficBytes / 1e9).toFixed(1) + " GB"} />
      </div>
      <Card title={t("user.dashboard.viewAll")}>
        <ul className="space-y-1">
          {data.slice(0, 5).map((instance) => (
            <li key={instance.id} className="flex items-center gap-2 text-sm">
              <StatusBadge
                tone={instance.observed_state === "running" ? "success" : instance.observed_state === "error" ? "danger" : "neutral"}
                labelKey="user.instances.state"
              />
              <Link to={`/instances/${instance.id}`} className="underline text-content">
                {instance.name}
              </Link>
              <span className="text-xs text-content-muted">{date(instance.created_at)}</span>
            </li>
          ))}
        </ul>
      </Card>
    </Page>
  );
}

export function InvoicesPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const money = useMoney();
  const date = useDate();
  const query = useQuery({ queryKey: ["/api/v1/invoices"], queryFn: () => listInvoices(apiClient) });

  if (query.isPending) {
    return <LoadingState />;
  }
  if (query.isError) {
    return <ErrorState messageKey={query.error.messageKey} requestId={query.error.requestId} />;
  }
  if (query.data.length === 0) {
    return <Page title={t("user.invoices.title")}><Card>{t("user.invoices.empty")}</Card></Page>;
  }

  return (
    <Page title={t("user.invoices.title")}>
      <div className="space-y-2">
        {query.data.map((detail: InvoiceDetail) => (
          <Card key={detail.invoice.id} title={`${t("user.invoices.no")}: ${detail.invoice.invoice_no}`}>
            <div className="flex flex-wrap items-center gap-2">
              <StatusBadge
                tone={detail.invoice.status === "paid" ? "success" : detail.invoice.status === "open" ? "warning" : "neutral"}
                labelKey={`user.invoiceStatus.${detail.invoice.status}`}
              />
              <span className="text-sm text-content">{money(detail.invoice.amount_minor, detail.invoice.currency)}</span>
              <span className="text-xs text-content-muted">{date(detail.invoice.created_at)}</span>
            </div>
          </Card>
        ))}
      </div>
    </Page>
  );
}

export function WalletPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const money = useMoney();
  const date = useDate();
  const query = useQuery({ queryKey: ["/api/v1/wallet"], queryFn: () => getWallet(apiClient) });

  if (query.isPending) {
    return <LoadingState />;
  }
  if (query.isError) {
    return <ErrorState messageKey={query.error.messageKey} requestId={query.error.requestId} />;
  }

  const { wallets, ledger } = query.data;
  if (wallets.length === 0 && ledger.length === 0) {
    return <Page title={t("user.wallet.title")}><Card>{t("user.wallet.empty")}</Card></Page>;
  }

  return (
    <Page title={t("user.wallet.title")}>
      <div className="grid gap-4 sm:grid-cols-2">
        {wallets.map((wallet) => (
          <Stat key={wallet.id} label={`${t("user.wallet.balance")} (${wallet.currency})`} value={money(wallet.available_balance_minor, wallet.currency)} />
        ))}
      </div>
      <Card title={t("user.wallet.movements")}>
        {ledger.length === 0 ? (
          <p className="text-sm text-content-muted">{t("user.wallet.empty")}</p>
        ) : (
          <ul className="space-y-1">
            {ledger.map((movement, index) => (
              <li key={index} className="flex flex-wrap items-center gap-2 text-sm">
                <span className={movement.direction === "credit" ? "text-status-success-content" : "text-status-danger-content"}>
                  {movement.direction === "credit" ? t("user.wallet.credit") : t("user.wallet.debit")}
                </span>
                <span className="text-content">{money(movement.amount_minor, movement.currency)}</span>
                <span className="text-xs text-content-muted">{movement.transaction_type}</span>
                <span className="text-xs text-content-muted">{date(movement.created_at)}</span>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </Page>
  );
}

export function NotificationsPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const date = useDate();
  const queryClient = useQueryClient();
  const { session } = useSession();
  const query = useQuery({ queryKey: ["/api/v1/notifications"], queryFn: () => listNotifications(apiClient) });
  const markRead = useMutation({
    mutationFn: (id: string) => markNotificationRead(apiClient, session?.csrf_token ?? "", id),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["/api/v1/notifications"] }),
  });

  if (query.isPending) {
    return <LoadingState />;
  }
  if (query.isError) {
    return <ErrorState messageKey={query.error.messageKey} requestId={query.error.requestId} />;
  }

  const { notifications, unread } = query.data;
  if (notifications.length === 0) {
    return <Page title={t("user.notifications.title")}><Card>{t("user.notifications.empty")}</Card></Page>;
  }

  return (
    <Page title={`${t("user.notifications.title")}${unread > 0 ? ` · ${t("user.notifications.unread")} ${unread}` : ""}`}>
      <div className="space-y-2">
        {notifications.map((notification) => (
          <Card key={notification.id}>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div>
                <p className="text-sm font-medium text-content">{t(notification.title_key)}</p>
                <p className="text-xs text-content-muted">{t(notification.message_key, notification.parameters)}</p>
                <p className="text-xs text-content-subtle">{date(notification.created_at)}</p>
              </div>
              {notification.read_at === null && (
                <Button variant="secondary" onClick={() => void markRead.mutateAsync(notification.id)}>
                  {t("user.notifications.markRead")}
                </Button>
              )}
            </div>
          </Card>
        ))}
      </div>
    </Page>
  );
}
