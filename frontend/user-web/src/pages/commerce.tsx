/**
 * The buying journey (docs/10): catalog, checkout, payment, orders.
 *
 * The plan chosen in the catalog is carried to checkout through the router's
 * state; the order is priced and created by the server, and the payment is
 * whatever the gateway's intent says — the client never computes a price.
 */

import { Button, Card, ErrorState, LoadingState, StatusBadge} from "@vps/shared";
import { useMutation, useQuery } from "../hooks.js";
import { useTranslation } from "react-i18next";
import type { ReactNode } from "react";
import { Navigate, useNavigate, useParams, useSearchParams } from "react-router-dom";

import { getOrder, listOrders, listProducts, startPayment } from "../api/endpoints.js";
import type { Order, Plan, Product } from "../api/types.js";
import type { ApiClient } from "@vps/shared";
import { useDate, useLocalizedText, useMoney } from "../format-hooks.js";
import { Page } from "../page-fragments.js";
import { useSession } from "../session-context.js";

export function CatalogPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const localized = useLocalizedText();
  const money = useMoney();
  const navigate = useNavigate();

  const query = useQuery({ queryKey: ["catalog"], queryFn: () => listProducts(apiClient) });

  if (query.isPending) {
    return <LoadingState />;
  }
  if (query.isError) {
    return <ErrorState messageKey={query.error.messageKey} requestId={query.error.requestId} onRetry={() => void query.refetch()} />;
  }
  if (query.data.length === 0) {
    return <Page title={t("user.catalog.title")}><Card>{t("user.catalog.empty")}</Card></Page>;
  }

  return (
    <Page title={t("user.catalog.title")} subtitle={t("user.catalog.subtitle")}>
      <div className="grid gap-4 md:grid-cols-2">
        {query.data.map((product) => (
          <ProductCard key={product.id} product={product} localized={localized} money={money} t={t} onSelect={(plan) => navigate("/checkout", { state: { plan } })} />
        ))}
      </div>
    </Page>
  );
}

function ProductCard({ product, localized, money, t, onSelect }: {
  product: Product;
  localized: (value: Record<string, string> | null | undefined, fallback: string) => string;
  money: (minor: number, currency: string) => string;
  t: (key: string) => string;
  onSelect: (plan: Plan) => void;
}): ReactNode {
  return (
    <Card title={localized(product.name_i18n, product.slug)} description={localized(product.description_i18n, "")}>
      <div className="space-y-2">
        {product.plans.map((plan) => (
          <div key={plan.id} className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border-subtle px-3 py-2">
            <div>
              <p className="text-sm font-medium text-content">
                {localized(plan.name_i18n, plan.slug)} · {t(`user.catalog.cycle.${plan.billing_cycle}`)}
              </p>
              <p className="text-xs text-content-muted">
                {plan.memory_mb} {t("user.catalog.specs.mb")} {t("user.catalog.specs.memory")} · {plan.disk_gb} {t("user.catalog.specs.gb")} {t("user.catalog.specs.disk")}
                {plan.traffic_gb !== null && plan.traffic_gb > 0
                  ? ` · ${plan.traffic_gb} ${t("user.catalog.specs.gb")} ${t("user.catalog.specs.traffic")}`
                  : ` · ${t("user.catalog.specs.traffic")} ${t("user.catalog.specs.unlimited")}`}
                {plan.bandwidth_mbps !== null ? ` · ${plan.bandwidth_mbps} ${t("user.catalog.specs.mbps")}` : ""}
              </p>
            </div>
            <div className="flex items-center gap-2">
              <span className="text-sm font-semibold text-content">{money(plan.price_minor, plan.currency)}</span>
              <Button onClick={() => onSelect(plan)}>{t("user.catalog.select")}</Button>
            </div>
          </div>
        ))}
      </div>
    </Card>
  );
}

export function CheckoutPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const money = useMoney();
  const { session } = useSession();
  const [searchParams] = useSearchParams();
  const planID = searchParams.get("plan");

  const catalog = useQuery({ queryKey: ["catalog"], queryFn: () => listProducts(apiClient) });
  const order = useMutation({
    mutationFn: async (plan: Plan) => {
      const data = await apiClient.request<{ order: Order }>("/api/v1/orders", {
        method: "POST",
        body: { items: [{ plan_id: plan.id, quantity: 1 }] },
        headers: { "X-CSRF-Token": session?.csrf_token ?? "" },
      });
      return data.order;
    },
  });
  const payment = useMutation({
    mutationFn: async (orderID: string) => startPayment(apiClient, session?.csrf_token ?? "", orderID, "fake"),
  });

  if (session === null) {
    return <Navigate to="/login" replace />;
  }

  const plan = planID === null ? null : catalog.data?.flatMap((p) => p.plans).find((p) => p.id === planID) ?? null;

  return (
    <Page title={t("user.checkout.title")}>
      {plan === null && order.data === undefined && <Card>{t("user.checkout.empty")}</Card>}
      {plan !== null && order.data === undefined && (
        <Card title={t("user.checkout.title")}>
          <p className="text-sm text-content">{money(plan.price_minor, plan.currency)}</p>
          <Button onClick={() => void order.mutateAsync(plan)}>{t("user.checkout.place")}</Button>
          {order.isError && (
            <p className="text-sm text-status-danger-content">{t(order.error.messageKey)}</p>
          )}
        </Card>
      )}
      {order.data !== undefined && (
        <Card title={t("user.orders.detail")}>
          <p className="text-sm text-content-muted">{t("user.orders.no")}: {order.data.order_no}</p>
          <p className="text-sm font-semibold text-content">{t("user.checkout.total")}: {money(order.data.total_minor, order.data.currency)}</p>
          {payment.data === undefined ? (
            <Button onClick={() => void payment.mutateAsync(order.data.id)}>{t("user.checkout.pay")}</Button>
          ) : (
            <div className="space-y-2">
              <p className="text-sm text-content">{t("user.checkout.payment.started")}</p>
              <a className="text-sm underline text-content" href={payment.data.pay_url} target="_blank" rel="noreferrer">
                {t("user.checkout.payment.open")}
              </a>
            </div>
          )}
          {payment.isError && <p className="text-sm text-status-danger-content">{t(payment.error.messageKey)}</p>}
        </Card>
      )}
    </Page>
  );
}

export function OrdersPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const money = useMoney();
  const date = useDate();
  const query = useQuery({ queryKey: ["/api/v1/orders"], queryFn: () => listOrders(apiClient) });

  if (query.isPending) {
    return <LoadingState />;
  }
  if (query.isError) {
    return <ErrorState messageKey={query.error.messageKey} requestId={query.error.requestId} />;
  }
  if (query.data.length === 0) {
    return <Page title={t("user.orders.title")}><Card>{t("user.orders.empty")}</Card></Page>;
  }

  return (
    <Page title={t("user.orders.title")}>
      <div className="space-y-2">
        {query.data.map((order) => (
          <Card key={order.id} title={`${t("user.orders.no")}: ${order.order_no}`}>
            <div className="flex flex-wrap items-center gap-2">
              <StatusBadge tone={order.status === "paid" ? "success" : order.status === "pending" ? "warning" : "neutral"} labelKey={`user.orderStatus.${order.status}`} />
              <span className="text-sm text-content">{money(order.total_minor, order.currency)}</span>
              <span className="text-xs text-content-muted">{date(order.created_at)}</span>
            </div>
          </Card>
        ))}
      </div>
    </Page>
  );
}

export function OrderDetailPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const money = useMoney();
  const date = useDate();
  const localized = useLocalizedText();
  const params = useParams();
  const { session } = useSession();
  const query = useQuery({
    queryKey: [`/api/v1/orders/${params.orderID}`],
    queryFn: () => getOrder(apiClient, params.orderID ?? ""),
    enabled: session !== null,
  });

  if (query.isPending) {
    return <LoadingState />;
  }
  if (query.isError) {
    return <ErrorState messageKey={query.error.messageKey} requestId={query.error.requestId} />;
  }
  const order = query.data;

  return (
    <Page title={`${t("user.orders.no")}: ${order.order_no}`}>
      <Card>
        <div className="space-y-2">
          <div className="flex items-center gap-2">
            <StatusBadge tone={order.status === "paid" ? "success" : order.status === "pending" ? "warning" : "neutral"} labelKey={`user.orderStatus.${order.status}`} />
            <span className="text-sm text-content">{money(order.total_minor, order.currency)}</span>
            <span className="text-xs text-content-muted">{date(order.created_at)}</span>
          </div>
          <ul className="space-y-1">
            {order.items.map((item) => (
              <li key={item.id} className="text-sm text-content-muted">
                {localized(item.plan_snapshot?.name_i18n, "")} ×{item.quantity} — {money(item.total_minor, order.currency)}
              </li>
            ))}
          </ul>
          {order.status === "pending" && session !== null && (
            <PayNow apiClient={apiClient} orderID={order.id} />
          )}
        </div>
      </Card>
    </Page>
  );
}

function PayNow({ apiClient, orderID }: { apiClient: ApiClient; orderID: string }): ReactNode {
  const { t } = useTranslation();
  const { session } = useSession();
  const payment = useMutation({
    mutationFn: () => startPayment(apiClient, session?.csrf_token ?? "", orderID, "fake"),
  });

  if (payment.data !== undefined) {
    return (
      <div className="space-y-1">
        <p className="text-sm text-content">{t("user.checkout.payment.started")}</p>
        <a className="text-sm underline text-content" href={payment.data.pay_url} target="_blank" rel="noreferrer">
          {t("user.checkout.payment.open")}
        </a>
      </div>
    );
  }
  return (
    <div className="space-y-1">
      <Button onClick={() => void payment.mutateAsync()}>{t("user.checkout.pay")}</Button>
      {payment.isError && <p className="text-sm text-status-danger-content">{t(payment.error.messageKey)}</p>}
    </div>
  );
}
