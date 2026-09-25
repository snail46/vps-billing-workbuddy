/**
 * The admin pages.
 *
 * One file, because the screens are one family: a table, a detail pane, a
 * permission. The dashboard comes first (docs/11): anomalies and health
 * before commerce. Every screen handles its states through the shared
 * components; a table that only looks right when the network is healthy is
 * not finished.
 */

import { Button, Card, ErrorState, LoadingState, StatusBadge, type ApiClient } from "@vps/shared";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import { useMutation, useQuery } from "../hooks.js";
import type { ReactNode } from "react";
import { Link, useParams } from "react-router-dom";

import { num, paths, str } from "../api.js";
import { useDate, useMoney } from "../hooks.js";
import { useAdminSession } from "../session-context.js";

type Row = Record<string, unknown>;

function Table({ head, rows, render }: { head: string[]; rows: Row[]; render: (row: Row) => ReactNode }): ReactNode {
  const { t } = useTranslation();
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-border-subtle text-left text-xs text-content-muted">
            {head.map((cell) => (
              <th key={cell} className="px-2 py-1.5 font-medium">
                {cell}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, index) => (
            <tr key={index} className="border-b border-border-subtle/50">
              {render(row)}
            </tr>
          ))}
          {rows.length === 0 && (
            <tr>
              <td colSpan={head.length} className="px-2 py-3 text-content-muted">
                {t("common.state.empty.title")}
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

export function Page({ title, subtitle, children }: { title: ReactNode; subtitle?: ReactNode; children: ReactNode }): ReactNode {
  return (
    <div className="space-y-3">
      <div>
        <h1 className="text-lg font-semibold text-content">{title}</h1>
        {subtitle !== undefined && <p className="text-xs text-content-muted">{subtitle}</p>}
      </div>
      {children}
    </div>
  );
}


export function DashboardPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const money = useMoney();
  const query = useQuery({
    queryKey: [paths.overview],
    queryFn: () => apiClient.request<Record<string, unknown>>(paths.overview),
    refetchInterval: 15_000,
  });

  if (query.isPending) {
    return <LoadingState />;
  }
  if (query.isError) {
    return <ErrorState messageKey={query.error.messageKey} requestId={query.error.requestId} onRetry={() => void query.refetch()} />;
  }
  const data = query.data;
  const alerts: Array<[string, number]> = [
    [t("admin.dashboard.alerts.failedOperations"), num(data, "failed_operations")],
    [t("admin.dashboard.alerts.nodesOffline"), num(data, "nodes_offline")],
    [t("admin.dashboard.alerts.capacityWarnings"), num(data, "capacity_warnings")],
    [t("admin.dashboard.alerts.suspendedUsers"), num(data, "suspended_users")],
  ];
  const byStatus = Array.isArray(data.payments_by_status) ? (data.payments_by_status as Row[]) : [];

  return (
    <Page title={t("admin.dashboard.title")} subtitle={t("admin.dashboard.subtitle")}>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {alerts.map(([label, value]) => (
          <Card key={label}>
            <p className="text-xs text-content-muted">{label}</p>
            <p className={value > 0 ? "text-2xl font-semibold text-status-danger-content" : "text-2xl font-semibold text-content"}>{value}</p>
          </Card>
        ))}
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <Card>
          <p className="text-xs text-content-muted">{t("admin.dashboard.health.runningOperations")}</p>
          <p className="text-xl font-semibold text-content">{num(data, "running_operations")}</p>
          <p className="mt-2 text-xs text-content-muted">{t("admin.dashboard.health.activeUsers")}</p>
          <p className="text-xl font-semibold text-content">{num(data, "active_users")}</p>
        </Card>
        <Card title={t("admin.dashboard.commerce.revenueTotal")}>
          <p className="text-xl font-semibold text-content">{money(num(data, "revenue_total_minor"), "CNY")}</p>
          <p className="mt-2 text-xs text-content-muted">{t("admin.dashboard.commerce.paymentsByStatus")}</p>
          <ul className="mt-1 space-y-0.5">
            {byStatus.map((row, index) => (
              <li key={index} className="text-sm text-content">
                {str(row, "status")}: {num(row, "count")}
              </li>
            ))}
          </ul>
        </Card>
      </div>
    </Page>
  );
}

export function UsersPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const day = useDate();
  const queryClient = useQueryClient();
  const { session } = useAdminSession();
  const users = useQuery({
    queryKey: [paths.users],
    queryFn: () => apiClient.request<{ users: Row[] }>(paths.users),
  });
  const setStatus = useMutation({
    mutationFn: (input: { id: string; action: "suspend" | "activate" }) =>
      apiClient.request(paths[input.action === "suspend" ? "suspendUser" : "activateUser"](input.id), {
        method: "POST",
        headers: { "X-CSRF-Token": session?.csrf_token ?? "" },
      }),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: [paths.users] }),
  });

  if (users.isPending) {
    return <LoadingState />;
  }
  if (users.isError) {
    return <ErrorState messageKey={users.error.messageKey} requestId={users.error.requestId} onRetry={() => void users.refetch()} />;
  }

  return (
    <Page title={t("admin.nav.users")}>
      <Card>
        <Table
          head={[t("admin.common.email"), t("admin.common.status"), t("admin.common.created"), t("admin.common.actions")]}
          rows={users.data.users}
          render={(row) => (
            <>
              <td className="px-2 py-1.5 text-content">{str(row, "email")}</td>
              <td className="px-2 py-1.5">
                <StatusBadge tone={str(row, "status") === "active" ? "success" : "danger"} labelKey="user.instances.state" />
                <span className="ml-1 text-content-muted">{str(row, "status")}</span>
              </td>
              <td className="px-2 py-1.5 text-content-muted">{day(str(row, "created_at"))}</td>
              <td className="px-2 py-1.5">
                {str(row, "status") === "active" ? (
                  <Button variant="secondary" onClick={() => void setStatus.mutateAsync({ id: str(row, "id"), action: "suspend" })}>
                    {t("admin.common.suspend")}
                  </Button>
                ) : (
                  <Button variant="secondary" onClick={() => void setStatus.mutateAsync({ id: str(row, "id"), action: "activate" })}>
                    {t("admin.common.activate")}
                  </Button>
                )}
              </td>
            </>
          )}
        />
      </Card>
    </Page>
  );
}

export function OperationsPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const day = useDate();
  const query = useQuery({
    queryKey: [paths.operations],
    queryFn: () => apiClient.request<{ operations: Row[] }>(paths.operations),
    refetchInterval: 10_000,
  });
  if (query.isPending) {
    return <LoadingState />;
  }
  if (query.isError) {
    return <ErrorState messageKey={query.error.messageKey} requestId={query.error.requestId} onRetry={() => void query.refetch()} />;
  }
  return (
    <Page title={t("admin.nav.operations")}>
      <Card>
        <Table
          head={[t("admin.common.resource"), t("admin.common.status"), t("admin.common.created"), ""]}
          rows={query.data.operations}
          render={(row) => (
            <>
              <td className="px-2 py-1.5 text-content">
                {str(row, "type")} / {str(row, "resource_type")}
              </td>
              <td className="px-2 py-1.5">
                <StatusBadge
                  tone={str(row, "status") === "succeeded" ? "success" : str(row, "status") === "failed" ? "danger" : "warning"}
                  labelKey={`operation.status.${str(row, "status")}`}
                />
              </td>
              <td className="px-2 py-1.5 text-content-muted">{day(str(row, "created_at"))}</td>
              <td className="px-2 py-1.5">
                <Link to={`/operations/${str(row, "id")}`} className="underline text-content">
                  {t("admin.common.resource")}
                </Link>
              </td>
            </>
          )}
        />
      </Card>
    </Page>
  );
}

export function TicketsPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const day = useDate();
  const queryClient = useQueryClient();
  const { session } = useAdminSession();
  const params = useParams();
  const tickets = useQuery({
    queryKey: [paths.tickets],
    queryFn: () => apiClient.request<{ tickets: Row[] }>(paths.tickets),
  });
  const detail = useQuery({
    queryKey: [paths.ticket(params.ticketID ?? "")],
    enabled: params.ticketID !== undefined,
    queryFn: () => apiClient.request<{ ticket: Row; messages: Row[] }>(paths.ticket(params.ticketID ?? "")),
  });
  const reply = useMutation({
    mutationFn: (message: string) =>
      apiClient.request(paths.ticketMessages(params.ticketID ?? ""), {
        method: "POST",
        body: { message },
        headers: { "X-CSRF-Token": session?.csrf_token ?? "" },
      }),
    onSuccess: () => void queryClient.invalidateQueries(),
  });
  const close = useMutation({
    mutationFn: () =>
      apiClient.request(paths.ticketClose(params.ticketID ?? ""), {
        method: "POST",
        headers: { "X-CSRF-Token": session?.csrf_token ?? "" },
      }),
    onSuccess: () => void queryClient.invalidateQueries(),
  });

  const statusTone = (status: string) => (status === "closed" ? "neutral" : status === "answered" ? "info" : "warning");

  if (params.ticketID !== undefined) {
    if (detail.isPending) {
      return <LoadingState />;
    }
    if (detail.isError) {
      return <ErrorState messageKey={detail.error.messageKey} requestId={detail.error.requestId} onRetry={() => void detail.refetch()} />;
    }
    const { ticket, messages } = detail.data;
    return (
      <Page title={str(ticket, "subject")} subtitle={`${t("admin.common.ticketNo")}: ${str(ticket, "ticket_no")} · ${str(ticket, "user_email")}`}>
        <Card>
          <ul className="space-y-2">
            {messages.map((message, index) => (
              <li key={index} className="rounded-md border border-border-subtle bg-surface-muted px-3 py-2">
                <p className="text-xs text-content-muted">
                  {str(message, "sender_type")} · {day(str(message, "created_at"))}
                </p>
                <p className="whitespace-pre-wrap text-sm text-content">{str(message, "message")}</p>
              </li>
            ))}
          </ul>
        </Card>
        {str(ticket, "status") !== "closed" && (
          <Card>
            <form
              className="space-y-2"
              onSubmit={(event) => {
                event.preventDefault();
                const input = event.currentTarget.elements.namedItem("message");
                if (input instanceof HTMLTextAreaElement && input.value !== "") {
                  void reply.mutateAsync(input.value).then(() => {
                    input.value = "";
                  });
                }
              }}
            >
              <textarea name="message" rows={3} className="w-full rounded-md border border-border-subtle bg-surface px-2 py-1.5 text-sm text-content" />
              <div className="flex gap-2">
                <Button type="submit">{t("admin.common.send")}</Button>
                <Button type="button" variant="secondary" onClick={() => void close.mutateAsync()}>
                  {t("admin.common.close")}
                </Button>
              </div>
            </form>
          </Card>
        )}
      </Page>
    );
  }

  if (tickets.isPending) {
    return <LoadingState />;
  }
  if (tickets.isError) {
    return <ErrorState messageKey={tickets.error.messageKey} requestId={tickets.error.requestId} onRetry={() => void tickets.refetch()} />;
  }
  return (
    <Page title={t("admin.nav.tickets")}>
      <Card>
        <Table
          head={[t("admin.common.ticketNo"), t("admin.common.email"), t("admin.common.status"), t("admin.common.updated"), ""]}
          rows={tickets.data.tickets}
          render={(row) => (
            <>
              <td className="px-2 py-1.5 text-content">{str(row, "ticket_no")}</td>
              <td className="px-2 py-1.5 text-content">{str(row, "user_email")}</td>
              <td className="px-2 py-1.5">
                <StatusBadge tone={statusTone(str(row, "status"))} labelKey={`user.tickets.status.${str(row, "status")}`} />
              </td>
              <td className="px-2 py-1.5 text-content-muted">{day(str(row, "updated_at"))}</td>
              <td className="px-2 py-1.5">
                <Link to={`/tickets/${str(row, "id")}`} className="underline text-content">
                  {t("admin.common.reply")}
                </Link>
              </td>
            </>
          )}
        />
      </Card>
    </Page>
  );
}
