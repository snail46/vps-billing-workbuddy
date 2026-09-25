/**
 * The admin tables that are pure reads: products, orders, payments, ledger,
 * subscriptions, instances, audit, admins, roles, settings. One list shape,
 * one permission each (ADR-012).
 */

import { Card, ErrorState, LoadingState, StatusBadge, type ApiClient } from "@vps/shared";
import { useTranslation } from "react-i18next";
import { useQuery } from "../hooks.js";
import type { ReactNode } from "react";
import { Link, useParams } from "react-router-dom";

import { paths, str } from "../api.js";
import { useDate, useMoney } from "../hooks.js";
import { Page } from "./core.js";

type Row = Record<string, unknown>;

function SimpleList({ apiClient, path, keyField, title, columns, rowLink }: {
  apiClient: ApiClient;
  path: string;
  keyField: string;
  title: ReactNode;
  columns: Array<{ label: string; render: (row: Row) => ReactNode }>;
  rowLink?: { prefix: string; label: ReactNode };
}): ReactNode {
  const query = useQuery({
    queryKey: [path],
    queryFn: () => apiClient.request<{ [k: string]: Row[] }>(path),
  });
  if (query.isPending) {
    return <LoadingState />;
  }
  if (query.isError) {
    return <ErrorState messageKey={query.error.messageKey} requestId={query.error.requestId} onRetry={() => void query.refetch()} />;
  }
  const rows = (Object.values(query.data)[0] ?? []) as Row[];
  return (
    <Page title={title}>
      <Card>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-border-subtle text-left text-xs text-content-muted">
                {columns.map((column) => (
                  <th key={column.label} className="px-2 py-1.5 font-medium">
                    {column.label}
                  </th>
                ))}
                {rowLink !== undefined && <th className="px-2 py-1.5 font-medium" />}
              </tr>
            </thead>
            <tbody>
              {rows.map((row, index) => (
                <tr key={index} className="border-b border-border-subtle/50">
                  {columns.map((column) => (
                    <td key={column.label} className="px-2 py-1.5">
                      {column.render(row)}
                    </td>
                  ))}
                  {rowLink !== undefined && (
                    <td className="px-2 py-1.5">
                      <Link to={`${rowLink.prefix}/${str(row, keyField)}`} className="underline text-content">
                        {rowLink.label}
                      </Link>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
    </Page>
  );
}

export function ProductsPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  return SimpleList({
    apiClient,
    path: paths.products,
    keyField: "id",
    title: t("admin.nav.products"),
    columns: [
      { label: t("admin.common.key"), render: (row) => str(row, "slug") },
      { label: t("admin.common.status"), render: (row) => str(row, "status") },
      {
        label: t("admin.common.plan"),
        render: (row) => {
          const plans = row.plans;
          return Array.isArray(plans) ? `${plans.length}` : "0";
        },
      },
    ],
  });
}

export function OrdersPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const money = useMoney();
  const day = useDate();
  return SimpleList({
    apiClient,
    path: paths.orders,
    keyField: "id",
    title: t("admin.nav.orders"),
    columns: [
      { label: t("admin.common.orderNo"), render: (row) => str(row, "order_no") },
      { label: t("admin.common.email"), render: (row) => str(row, "user_email") },
      { label: t("admin.common.status"), render: (row) => str(row, "status") },
      { label: t("admin.common.amount"), render: (row) => money(Number(row.total_minor ?? 0), str(row, "currency")) },
      { label: t("admin.common.created"), render: (row) => day(str(row, "created_at")) },
    ],
    rowLink: { prefix: "/orders", label: t("admin.nav.orders") },
  });
}

export function PaymentsPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const money = useMoney();
  const day = useDate();
  return SimpleList({
    apiClient,
    path: paths.payments,
    keyField: "id",
    title: t("admin.nav.payments"),
    columns: [
      { label: t("admin.common.key"), render: (row) => str(row, "payment_no") },
      { label: t("admin.common.orderNo"), render: (row) => str(row, "order_no") },
      { label: t("admin.common.gateways"), render: (row) => str(row, "gateway") },
      { label: t("admin.common.status"), render: (row) => str(row, "status") },
      { label: t("admin.common.amount"), render: (row) => money(Number(row.amount_minor ?? 0), str(row, "currency")) },
      { label: t("admin.common.created"), render: (row) => day(str(row, "created_at")) },
    ],
  });
}

export function LedgerPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const money = useMoney();
  const day = useDate();
  return SimpleList({
    apiClient,
    path: paths.ledger,
    keyField: "id",
    title: t("admin.nav.ledger"),
    columns: [
      { label: t("admin.common.action"), render: (row) => str(row, "transaction_type") },
      { label: t("admin.common.status"), render: (row) => str(row, "direction") },
      { label: t("admin.common.amount"), render: (row) => money(Number(row.amount_minor ?? 0), str(row, "currency")) },
      { label: t("admin.common.created"), render: (row) => day(str(row, "created_at")) },
    ],
  });
}

export function SubscriptionsPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const money = useMoney();
  const day = useDate();
  return SimpleList({
    apiClient,
    path: paths.subscriptions,
    keyField: "id",
    title: t("admin.nav.subscriptions"),
    columns: [
      { label: t("admin.common.email"), render: (row) => str(row, "user_email") },
      { label: t("admin.common.status"), render: (row) => str(row, "status") },
      { label: t("admin.common.cycle"), render: (row) => str(row, "billing_cycle") },
      { label: t("admin.common.amount"), render: (row) => money(Number(row.price_minor ?? 0), str(row, "currency")) },
      { label: t("admin.common.expired"), render: (row) => day(str(row, "current_period_end")) },
    ],
  });
}

export function AdminAuditPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const day = useDate();
  return SimpleList({
    apiClient,
    path: paths.audit,
    keyField: "id",
    title: t("admin.nav.audit"),
    columns: [
      { label: t("admin.common.actor"), render: (row) => str(row, "actor_type") },
      { label: t("admin.common.action"), render: (row) => str(row, "action") },
      { label: t("admin.common.resource"), render: (row) => str(row, "resource_type") },
      { label: t("admin.common.created"), render: (row) => day(str(row, "created_at")) },
    ],
  });
}

export function AdminsPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  return SimpleList({
    apiClient,
    path: paths.admins,
    keyField: "id",
    title: t("admin.nav.admins"),
    columns: [
      { label: t("admin.common.email"), render: (row) => str(row, "email") },
      { label: t("admin.common.status"), render: (row) => str(row, "status") },
      { label: t("admin.common.roles"), render: (row) => (Array.isArray(row.roles) ? row.roles.join(", ") : "—") },
    ],
  });
}

export function RolesPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  return SimpleList({
    apiClient,
    path: paths.roles,
    keyField: "id",
    title: t("admin.nav.roles"),
    columns: [
      { label: t("admin.common.key"), render: (row) => str(row, "key") },
      { label: t("admin.common.permissions"), render: (row) => (Array.isArray(row.permissions) ? `${row.permissions.length}` : "0") },
    ],
  });
}

export function SettingsPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  return SimpleList({
    apiClient,
    path: paths.settings,
    keyField: "key",
    title: t("admin.nav.settings"),
    columns: [
      { label: t("admin.common.key"), render: (row) => str(row, "key") },
      { label: t("admin.common.value"), render: (row) => str(row, "value") },
    ],
  });
}

export function AdminInstanceDetailPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const day = useDate();
  const params = useParams();
  const query = useQuery({
    queryKey: [paths.instance(params.instanceID ?? "")],
    queryFn: () => apiClient.request<Record<string, unknown>>(paths.instance(params.instanceID ?? "")),
  });
  if (query.isPending) {
    return <LoadingState />;
  }
  if (query.isError) {
    return <ErrorState messageKey={query.error.messageKey} requestId={query.error.requestId} onRetry={() => void query.refetch()} />;
  }
  const detail = query.data.instance as Row | undefined;
  const instance = detail ?? {};
  return (
    <Page title={`${t("admin.common.instance")}: ${str(instance, "name")}`}>
      <Card>
        <div className="grid gap-2 text-sm sm:grid-cols-2">
          <p>{t("admin.common.email")}: {str(instance, "user_email")}</p>
          <p>{t("admin.common.status")}: {str(instance, "subscription_status")}</p>
          <p>{t("admin.common.node")}: {str(instance, "node_name")}</p>
          <p>{t("admin.common.provider")}: {str(instance, "provider_name")}</p>
          <p>{t("admin.common.observed")}: {str(instance, "observed_state")}</p>
          <p>{t("admin.common.desired")}: {str(instance, "desired_state")}</p>
          <p>
            {str(instance, "cpu_cores_text")} {t("admin.common.cores")} · {str(instance, "memory_mb")} {t("admin.common.mb")} · {str(instance, "disk_gb")} {t("admin.common.gb")}
          </p>
          <p>{t("admin.common.created")}: {day(str(instance, "created_at"))}</p>
        </div>
      </Card>
      <Card title={t("admin.nav.operations")}>
        <OperationsMini rows={Array.isArray(query.data.operations) ? (query.data.operations as Row[]) : []} />
      </Card>
      <Card title={t("admin.nav.audit")}>
        <ul className="space-y-1 text-sm">
          {(Array.isArray(query.data.audit) ? (query.data.audit as Row[]) : []).map((row, index) => (
            <li key={index} className="text-content-muted">
              {str(row, "actor_type")} · {str(row, "action")} · {day(str(row, "created_at"))}
            </li>
          ))}
        </ul>
      </Card>
    </Page>
  );
}

function OperationsMini({ rows }: { rows: Row[] }): ReactNode {
  const day = useDate();
  return (
    <ul className="space-y-1 text-sm">
      {rows.map((row, index) => (
        <li key={index} className="flex items-center gap-2">
          <StatusBadge
            tone={str(row, "status") === "succeeded" ? "success" : str(row, "status") === "failed" ? "danger" : "warning"}
            labelKey={`operation.status.${str(row, "status")}`}
          />
          <span className="text-content">{str(row, "type")}</span>
          <span className="text-content-muted">{day(str(row, "created_at"))}</span>
        </li>
      ))}
    </ul>
  );
}

export function AdminOrderDetailPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const money = useMoney();
  const params = useParams();
  const query = useQuery({
    queryKey: [paths.order(params.orderID ?? "")],
    queryFn: () => apiClient.request<{ order: Row; items: Row[]; payments: Row[] }>(paths.order(params.orderID ?? "")),
  });
  if (query.isPending) {
    return <LoadingState />;
  }
  if (query.isError) {
    return <ErrorState messageKey={query.error.messageKey} requestId={query.error.requestId} onRetry={() => void query.refetch()} />;
  }
  const { order, items, payments } = query.data;
  return (
    <Page title={`${t("admin.common.orderNo")}: ${str(order, "order_no")}`}>
      <Card>
        <p className="text-sm text-content">
          {str(order, "user_email")} · {str(order, "status")} · {money(Number(order.total_minor ?? 0), str(order, "currency"))}
        </p>
        <ul className="mt-2 space-y-1 text-sm text-content-muted">
          {items.map((item, index) => (
            <li key={index}>×{str(item, "quantity")} — {money(Number(item.total_minor ?? 0), str(order, "currency"))}</li>
          ))}
        </ul>
        <ul className="mt-2 space-y-1 text-sm">
          {payments.map((payment, index) => (
            <li key={index}>
              {str(payment, "payment_no")} · {str(payment, "gateway")} · {str(payment, "status")}
            </li>
          ))}
        </ul>
      </Card>
    </Page>
  );
}

export function AdminInstancesPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const day = useDate();
  return SimpleList({
    apiClient,
    path: paths.instances,
    keyField: "id",
    title: t("admin.nav.instances"),
    columns: [
      { label: t("admin.common.instance"), render: (row) => str(row, "name") },
      { label: t("admin.common.email"), render: (row) => str(row, "user_email") },
      { label: t("admin.common.observed"), render: (row) => str(row, "observed_state") },
      { label: t("admin.common.node"), render: (row) => str(row, "node_name") },
      { label: t("admin.common.provider"), render: (row) => str(row, "provider_name") },
      { label: t("admin.common.created"), render: (row) => day(str(row, "created_at")) },
    ],
    rowLink: { prefix: "/instances", label: t("admin.common.instance") },
  });
}
