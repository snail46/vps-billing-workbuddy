/**
 * The instances screens (docs/10): the list and the detail with its four
 * tabs — overview, network, traffic, activity. The activity tab is the
 * operation stream: a refresh re-derives the progress from the operation row,
 * so what the customer sees is the record, not a local memory of it.
 */

import { Button, Card, ErrorState, LoadingState, OperationProgress, StatusBadge, type ApiClient } from "@vps/shared";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import { useMutation, useQuery } from "../hooks.js";
import { useState, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";

import { getInstance, getOperation, listInstances, reinstallInstance, restartInstance } from "../api/endpoints.js";
import type { InstanceDetail } from "../api/types.js";
import { API_BASE_URL } from "../config.js";
import { operationToView, operationToViewWithoutSteps, useOperationStream } from "../hooks.js";
import { useDate } from "../format-hooks.js";
import { Page } from "../page-fragments.js";
import { useSession } from "../session-context.js";

export function InstancesPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const date = useDate();
  const query = useQuery({ queryKey: ["/api/v1/instances"], queryFn: () => listInstances(apiClient) });

  if (query.isPending) {
    return <LoadingState />;
  }
  if (query.isError) {
    return <ErrorState messageKey={query.error.messageKey} requestId={query.error.requestId} />;
  }
  if (query.data.length === 0) {
    return (
      <Page title={t("user.instances.title")}>
        <Card>
          <p className="text-sm text-content">{t("user.dashboard.empty.title")}</p>
          <p className="text-xs text-content-muted">{t("user.instances.empty")}</p>
          <Link to="/catalog" className="text-sm underline text-content">
            {t("user.dashboard.empty.action")}
          </Link>
        </Card>
      </Page>
    );
  }

  return (
    <Page title={t("user.instances.title")}>
      <div className="grid gap-4 md:grid-cols-2">
        {query.data.map((instance) => (
          <Card
            key={instance.id}
            title={instance.name}
            action={
              <StatusBadge
                tone={instance.observed_state === "running" ? "success" : instance.observed_state === "error" ? "danger" : "neutral"}
                labelKey="user.instances.state"
              />
            }
          >
            <p className="text-xs text-content-muted">
              {t("user.instances.createdAt")}: {date(instance.created_at)}
            </p>
            <Link to={`/instances/${instance.id}`} className="text-sm underline text-content">
              {t("user.instances.detail")}
            </Link>
          </Card>
        ))}
      </div>
    </Page>
  );
}

const TABS = ["overview", "network", "traffic", "activity"] as const;
type Tab = (typeof TABS)[number];

export function InstanceDetailPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const params = useParams();
  const { session } = useSession();
  const [tab, setTab] = useState<Tab>("overview");
  const query = useQuery({
    queryKey: [`/api/v1/instances/${params.instanceID}`],
    queryFn: () => getInstance(apiClient, params.instanceID ?? ""),
    enabled: session !== null,
    // The observed state moves on its own; the page keeps itself honest.
    refetchInterval: 10_000,
  });

  if (session === null || query.isPending) {
    return <LoadingState />;
  }
  if (query.isError) {
    if (query.error.status === 404) {
      return (
        <Page title={t("user.instances.detail")}>
          <Card>{t("user.instances.notFound")}</Card>
        </Page>
      );
    }
    return <ErrorState messageKey={query.error.messageKey} requestId={query.error.requestId} />;
  }
  const instance = query.data;

  return (
    <Page title={`${t("user.instances.detail")}: ${instance.name}`}>
      <div className="flex flex-wrap gap-1">
        {TABS.map((key) => (
          <button
            key={key}
            type="button"
            onClick={() => setTab(key)}
            className={
              tab === key
                ? "rounded-md bg-surface-muted px-3 py-1.5 text-sm font-medium text-content"
                : "rounded-md px-3 py-1.5 text-sm text-content-muted hover:bg-surface-muted"
            }
          >
            {t(`user.instances.tabs.${key}`)}
          </button>
        ))}
      </div>
      {tab === "overview" && <OverviewTab apiClient={apiClient} instance={instance} />}
      {tab === "network" && <NetworkTab instance={instance} />}
      {tab === "traffic" && <TrafficTab instance={instance} />}
      {tab === "activity" && <ActivityTab apiClient={apiClient} instance={instance} />}
    </Page>
  );
}

function OverviewTab({ apiClient, instance }: { apiClient: ApiClient; instance: InstanceDetail }): ReactNode {
  const { t } = useTranslation();
  const { session } = useSession();
  const queryClient = useQueryClient();
  const [image, setImage] = useState("");
  const [confirming, setConfirming] = useState(false);

  const invalidate = () => void queryClient.invalidateQueries();
  const restart = useMutation({
    mutationFn: () => restartInstance(apiClient, session?.csrf_token ?? "", instance.id),
    onSuccess: invalidate,
  });
  const reinstall = useMutation({
    mutationFn: () => reinstallInstance(apiClient, session?.csrf_token ?? "", instance.id, image),
    onSuccess: invalidate,
  });

  return (
    <Card title={t("user.instances.spec")}>
      <div className="space-y-2 text-sm text-content">
        <div className="flex flex-wrap items-center gap-2">
          <span>{t("user.instances.observed")}:</span>
          <StatusBadge
            tone={instance.observed_state === "running" ? "success" : instance.observed_state === "error" ? "danger" : "neutral"}
            labelKey="user.instances.state"
          />
          <span className="text-content-muted">{t("user.instances.desired")}: {instance.desired_state}</span>
        </div>
        <p className="text-content-muted">
          {instance.cpu_cores} {t("user.catalog.specs.cores")} · {t("user.catalog.specs.memory")} {instance.memory_mb} {t("user.catalog.specs.mb")} · {t("user.catalog.specs.disk")} {instance.disk_gb} {t("user.catalog.specs.gb")}
        </p>
        <div className="flex flex-wrap items-center gap-2 border-t border-border-subtle pt-2">
          <Button variant="secondary" disabled={instance.observed_state !== "running" || restart.isPending} onClick={() => void restart.mutateAsync()}>
            {t("user.instances.actions.restart")}
          </Button>
          {restart.isError && <span className="text-xs text-status-danger-content">{t(restart.error.messageKey)}</span>}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <input
            value={image}
            onChange={(event) => setImage(event.target.value)}
            placeholder={t("user.instances.actions.imagePlaceholder")}
            className="rounded-md border border-border-subtle bg-surface px-2 py-1.5 text-sm text-content"
          />
          <Button
            variant="danger"
            disabled={instance.observed_state !== "running" || reinstall.isPending || image === ""}
            onClick={() => {
              if (confirming) {
                void reinstall.mutateAsync();
                setConfirming(false);
              } else {
                setConfirming(true);
              }
            }}
          >
            {t("user.instances.actions.reinstall")}
          </Button>
          {confirming && (
            <span className="text-xs text-status-warning-content">{t("user.instances.actions.confirmReinstall")}</span>
          )}
          {reinstall.isError && <span className="text-xs text-status-danger-content">{t(reinstall.error.messageKey)}</span>}
        </div>
      </div>
    </Card>
  );
}

function NetworkTab({ instance }: { instance: InstanceDetail }): ReactNode {
  const { t } = useTranslation();
  return (
    <Card title={t("user.instances.addresses")}>
      <div className="space-y-2">
        {instance.networks.length === 0 && <p className="text-sm text-content-muted">{t("common.unavailable")}</p>}
        {instance.networks.map((network, index) => (
          <p key={index} className="text-sm text-content">
            {network.type}: {network.address ?? "—"}
          </p>
        ))}
        <div className="border-t border-border-subtle pt-2">
          <p className="text-sm font-medium text-content">{t("user.instances.portForwards")}</p>
          {instance.port_forwards.length === 0 && <p className="text-sm text-content-muted">{t("user.instances.noPortForwards")}</p>}
          {instance.port_forwards.map((forward, index) => (
            <p key={index} className="text-sm text-content-muted">
              {forward.public_ip}:{forward.public_port} → {forward.guest_port} ({forward.protocol})
            </p>
          ))}
        </div>
      </div>
    </Card>
  );
}

function TrafficTab({ instance }: { instance: InstanceDetail }): ReactNode {
  const { t } = useTranslation();
  const date = useDate();
  return (
    <Card title={t("user.instances.traffic")}>
      {instance.traffic.length === 0 ? (
        <p className="text-sm text-content-muted">{t("common.unavailable")}</p>
      ) : (
        <div className="space-y-2">
          {instance.traffic.map((row, index) => (
            <div key={index} className="text-sm text-content">
              <p className="text-xs text-content-muted">
                {t("user.instances.period")}: {date(row.period_start)} — {date(row.period_end)}
              </p>
              <p>
                {t("user.instances.rx")}: {row.rx_bytes} {t("user.instances.bytes")} · {t("user.instances.tx")}: {row.tx_bytes} {t("user.instances.bytes")}
              </p>
            </div>
          ))}
        </div>
      )}
    </Card>
  );
}

function ActivityTab({ apiClient, instance }: { apiClient: ApiClient; instance: InstanceDetail }): ReactNode {
  const { t } = useTranslation();
  const { session } = useSession();
  // The live view of the machine's open workflow. The operation identifier
  // comes from the detail payload itself, so a refresh lands here and picks
  // the workflow back up — the row is the state, the stream is its voice.
  const query = useQuery({
    queryKey: [`/api/v1/operations`, instance.open_operation?.id ?? null],
    enabled: session !== null && instance.open_operation !== null,
    queryFn: () => getOperation(apiClient, instance.open_operation?.id ?? ""),
    refetchInterval: false,
  });
  const stream = useOperationStream(apiClient, API_BASE_URL, instance.open_operation?.id ?? null);

  if (instance.open_operation === null) {
    return (
      <Card title={t("user.instances.tabs.activity")}>
        <p className="text-sm text-content-muted">{t("user.notifications.empty")}</p>
      </Card>
    );
  }
  // The stream pushes the bare operation; the query carries the steps. Either
  // way the view is derived, never stored.
  const view = stream.operation !== null
    ? operationToViewWithoutSteps(stream.operation)
    : query.data !== undefined
      ? operationToView(query.data)
      : null;
  if (view === null) {
    return <LoadingState />;
  }
  return (
    <Card title={t("user.instances.tabs.activity")}>
      <OperationProgress operation={view} />
    </Card>
  );
}
