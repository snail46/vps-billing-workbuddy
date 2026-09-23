import {
  ApiError,
  Card,
  EmptyState,
  ErrorState,
  LoadingState,
  PartialErrorNotice,
  StatusBadge,
  formatDateTime,
  healthStatusLabelKey,
  healthStatusTone,
  splitDuration,
  type ApiClient,
  type HealthCheckReport,
  type HealthReport,
  type SupportedLocale,
} from "@vps/shared";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

/**
 * Foundational platform status.
 *
 * docs/11 makes the operations console's first job "find and locate anomalies
 * quickly", so unlike the user client this view shows the raw diagnostic text
 * each dependency reported and the correlation identifiers, rather than only
 * whether the platform is up.
 */
export function PlatformStatus({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t, i18n } = useTranslation();

  const query = useQuery({
    queryKey: ["health", "ready"],
    queryFn: () => apiClient.fetchHealthReport(),
  });

  const locale = resolveLocale(i18n.language);
  const refetch = (): void => {
    void query.refetch();
  };

  return (
    <Card
      title={t("health.title")}
      description={t("health.subtitle")}
      action={
        <StatusBadge
          tone={query.isSuccess ? healthStatusTone(query.data.status) : "neutral"}
          labelKey={query.isSuccess ? healthStatusLabelKey(query.data.status) : "common.state.loading"}
        />
      }
    >
      {query.isPending ? (
        <LoadingState />
      ) : query.isError ? (
        <ErrorState
          messageKey={messageKeyOf(query.error)}
          requestId={requestIdOf(query.error)}
          onRetry={refetch}
        />
      ) : (
        <DependencyTable
          report={query.data}
          locale={locale}
          onRetry={refetch}
        />
      )}
    </Card>
  );
}

function DependencyTable({
  report,
  locale,
  onRetry,
}: {
  report: HealthReport;
  locale: SupportedLocale;
  onRetry: () => void;
}): ReactNode {
  const { t } = useTranslation();

  const checks = report.checks ?? [];
  const failing = checks.filter((check) => check.status === "down");
  const duration = splitDuration(report.uptime_seconds);

  return (
    <div className="space-y-4">
      <dl className="grid grid-cols-2 gap-x-6 gap-y-2 lg:grid-cols-4">
        <Field labelKey="health.field.environment" value={report.environment} />
        <Field labelKey="health.field.version" value={report.version} />
        <Field labelKey="health.field.commit" value={report.commit} />
        <Field
          labelKey="health.field.uptime"
          value={t(`health.uptime.${duration.unit}`, { count: duration.count })}
        />
      </dl>

      <p className="text-xs text-content-subtle">
        {t("health.field.checkedAt")}
        {": "}
        {formatDateTime(report.time, { locale })}
      </p>

      {/* The page is usable while some dependencies are unhealthy. */}
      {failing.length > 0 && <PartialErrorNotice messageKey="health.status.down" onRetry={onRetry} />}

      <section>
        <h3 className="mb-2 text-xs font-semibold text-content-muted">{t("health.checks.title")}</h3>
        {checks.length === 0 ? (
          <EmptyState />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-left text-xs">
              <thead>
                <tr className="border-b border-border-subtle text-content-subtle">
                  <th className="px-3 py-2 font-medium">{t("health.checks.column.name")}</th>
                  <th className="px-3 py-2 font-medium">{t("health.checks.column.status")}</th>
                  <th className="px-3 py-2 font-medium">{t("health.checks.column.latency")}</th>
                  <th className="px-3 py-2 font-medium">{t("health.checks.column.diagnostic")}</th>
                </tr>
              </thead>
              <tbody>
                {checks.map((check) => (
                  <CheckRow key={check.name} check={check} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  );
}

function CheckRow({ check }: { check: HealthCheckReport }): ReactNode {
  const { t } = useTranslation();

  return (
    <tr className="border-b border-border-subtle align-top">
      <td className="px-3 py-2 font-mono text-content">{check.name}</td>
      <td className="px-3 py-2">
        <StatusBadge tone={healthStatusTone(check.status)} labelKey={healthStatusLabelKey(check.status)} />
      </td>
      <td className="px-3 py-2 text-content-subtle">
        {t("health.checks.latency", { ms: check.latency_ms })}
      </td>
      {/*
        docs/11 requires administrators to reach the raw provider diagnostic.
        For a dependency check the reported error text is that diagnostic, so it
        is shown verbatim rather than being hidden behind a generic message —
        the platform's own probe output is not a secret from the operator.
      */}
      <td className="px-3 py-2 font-mono break-all text-content-muted">
        {check.error ?? ""}
      </td>
    </tr>
  );
}

function Field({ labelKey, value }: { labelKey: string; value: string }): ReactNode {
  const { t } = useTranslation();

  return (
    <div className="space-y-0.5">
      <dt className="text-xs text-content-subtle">{t(labelKey)}</dt>
      <dd className="font-mono text-xs text-content">{value}</dd>
    </div>
  );
}

function messageKeyOf(error: unknown): string {
  return error instanceof ApiError ? error.messageKey : "errors.unknown";
}

function requestIdOf(error: unknown): string {
  return error instanceof ApiError ? error.requestId : "";
}

function resolveLocale(language: string): SupportedLocale {
  return language === "en-US" ? "en-US" : "zh-CN";
}
