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
  type HealthReport,
  type SupportedLocale,
} from "@vps/shared";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

/**
 * The single screen of the Phase 0 foundation.
 *
 * It exists to prove the stack end to end: a real request through the shared API
 * client, the response envelope and its error model, the i18n resources, the
 * design tokens, and the page states docs/10 requires. Every state rendered here
 * is reached by a real condition rather than being simulated.
 */
export function FoundationOverview({ apiClient }: { apiClient: ApiClient }): ReactNode {
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
    <Card title={t("health.title")} description={t("health.subtitle")}>
      {query.isPending ? (
        <LoadingState />
      ) : query.isError ? (
        <ErrorState
          messageKey={messageKeyOf(query.error)}
          requestId={requestIdOf(query.error)}
          onRetry={refetch}
        />
      ) : (
        <LoadedReport report={query.data} locale={locale} onRetry={refetch} />
      )}
    </Card>
  );
}

function LoadedReport({
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
      <div className="flex flex-wrap items-center gap-3">
        <StatusBadge
          tone={healthStatusTone(report.status)}
          labelKey={healthStatusLabelKey(report.status)}
        />
        <span className="text-xs text-content-subtle">
          {t("health.field.checkedAt")}
          {": "}
          {formatDateTime(report.time, { locale })}
        </span>
      </div>

      {/*
        The report loaded while some dependencies are unhealthy: the page is
        usable but part of its content is not. That is the partial_error state
        docs/10 requires, and it is deliberately an inline notice instead of a
        full-page error so the working content stays visible.
      */}
      {failing.length > 0 && (
        <PartialErrorNotice
          messageKey="health.status.down"
          onRetry={onRetry}
        />
      )}

      <dl className="grid grid-cols-1 gap-x-6 gap-y-2 sm:grid-cols-2">
        <Field labelKey="health.field.environment" value={report.environment} />
        <Field labelKey="health.field.version" value={report.version} />
        <Field labelKey="health.field.commit" value={report.commit} />
        <Field
          labelKey="health.field.uptime"
          value={t(`health.uptime.${duration.unit}`, { count: duration.count })}
        />
      </dl>

      <section>
        <h3 className="mb-2 text-xs font-semibold text-content-muted">
          {t("health.checks.title")}
        </h3>
        {checks.length === 0 ? (
          <EmptyState />
        ) : (
          <ul className="divide-y divide-border-subtle rounded-md border border-border-subtle">
            {checks.map((check) => (
              <li key={check.name} className="flex items-center justify-between gap-3 px-3 py-2">
                <span className="font-mono text-xs text-content">{check.name}</span>
                <span className="flex items-center gap-3">
                  <span className="text-xs text-content-subtle">
                    {t("health.checks.latency", { ms: check.latency_ms })}
                  </span>
                  <StatusBadge
                    tone={healthStatusTone(check.status)}
                    labelKey={healthStatusLabelKey(check.status)}
                  />
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function Field({ labelKey, value }: { labelKey: string; value: string }): ReactNode {
  const { t } = useTranslation();

  return (
    <div className="flex justify-between gap-3 border-b border-border-subtle pb-1 sm:border-none sm:pb-0">
      <dt className="text-xs text-content-subtle">{t(labelKey)}</dt>
      <dd className="font-mono text-xs text-content">{value}</dd>
    </div>
  );
}

/**
 * Extracts the i18n key from a failure.
 *
 * Falls back to the generic key so an unrecognised failure still renders an
 * explanation instead of an empty message — docs/19 classifies a critical error
 * with no feedback as a release blocker.
 */
function messageKeyOf(error: unknown): string {
  return error instanceof ApiError ? error.messageKey : "errors.unknown";
}

function requestIdOf(error: unknown): string {
  return error instanceof ApiError ? error.requestId : "";
}

function resolveLocale(language: string): SupportedLocale {
  return language === "en-US" ? "en-US" : "zh-CN";
}
