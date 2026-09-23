import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "./Button.js";

/**
 * Page-state components.
 *
 * docs/10 and docs/11 require every page and core card to handle six states:
 * loading, loaded, empty, error, partial_error and permission_denied. "Loaded"
 * is simply the content a page renders, so these five components plus the
 * absence of all of them cover the requirement.
 *
 * They are shared rather than per-page because the alternative — six hand-rolled
 * variants per screen — is how the requirement quietly stops holding.
 */

interface StateShellProps {
  tone?: "neutral" | "danger" | "warning";
  title: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
  /** Correlation identifier, shown so a failure can be quoted to support. */
  requestId?: string;
}

function StateShell({ tone = "neutral", title, description, action, requestId }: StateShellProps): ReactNode {
  const { t } = useTranslation();

  const borderClass =
    tone === "danger"
      ? "border-status-danger-border"
      : tone === "warning"
        ? "border-status-warning-border"
        : "border-border-subtle";

  return (
    <div
      role={tone === "danger" ? "alert" : "status"}
      className={`flex flex-col items-start gap-2 rounded-lg border ${borderClass} bg-surface-muted px-4 py-6`}
    >
      <p className="text-sm font-medium text-content">{title}</p>
      {description !== undefined && <p className="text-xs text-content-muted">{description}</p>}
      {requestId !== undefined && requestId !== "" && (
        <p className="font-mono text-xs text-content-subtle">
          {t("common.requestId")}
          {": "}
          {requestId}
        </p>
      )}
      {action}
    </div>
  );
}

/** Placeholder shown while content is being fetched. */
export function LoadingState(): ReactNode {
  const { t } = useTranslation();

  return (
    <div role="status" aria-label={t("common.state.loading")} className="space-y-2 px-4 py-6">
      <div className="h-3 w-1/3 animate-pulse rounded bg-surface-sunken" />
      <div className="h-3 w-2/3 animate-pulse rounded bg-surface-sunken" />
      <div className="h-3 w-1/2 animate-pulse rounded bg-surface-sunken" />
    </div>
  );
}

/** Shown when a request succeeded but produced nothing. */
export function EmptyState({ action }: { action?: ReactNode }): ReactNode {
  const { t } = useTranslation();

  return (
    <StateShell
      title={t("common.state.empty.title")}
      description={t("common.state.empty.description")}
      {...(action === undefined ? {} : { action })}
    />
  );
}

/** Props for {@link ErrorState}. */
export interface ErrorStateProps {
  /** i18n key describing the failure, typically taken from an ApiError. */
  messageKey: string;
  /** Correlation identifier from the failed response, when one was received. */
  requestId?: string;
  onRetry?: () => void;
}

/** Shown when the content could not be fetched at all. */
export function ErrorState({ messageKey, requestId, onRetry }: ErrorStateProps): ReactNode {
  const { t } = useTranslation();

  return (
    <StateShell
      tone="danger"
      title={t("common.state.error.title")}
      description={t(messageKey, { defaultValue: t("errors.unknown") })}
      {...(requestId === undefined ? {} : { requestId })}
      {...(onRetry === undefined
        ? {}
        : {
            action: (
              <Button variant="secondary" onClick={onRetry}>
                {t("common.actions.retry")}
              </Button>
            ),
          })}
    />
  );
}

/**
 * Shown when part of a screen loaded and part did not.
 *
 * Deliberately an inline notice rather than a full-page state: the point of
 * partial_error is that usable content is still on screen, and covering it would
 * discard information the user can act on.
 */
export function PartialErrorNotice({
  messageKey,
  onRetry,
}: {
  messageKey: string;
  onRetry?: () => void;
}): ReactNode {
  const { t } = useTranslation();

  return (
    <div
      role="alert"
      className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-status-warning-border bg-status-warning-surface px-3 py-2"
    >
      <div>
        <p className="text-xs font-medium text-status-warning">
          {t("common.state.partialError.title")}
        </p>
        <p className="text-xs text-content-muted">
          {t(messageKey, { defaultValue: t("common.state.partialError.description") })}
        </p>
      </div>
      {onRetry !== undefined && (
        <Button variant="ghost" onClick={onRetry}>
          {t("common.actions.retry")}
        </Button>
      )}
    </div>
  );
}

/**
 * Shown when the backend refused the request.
 *
 * docs/14 requires authorisation to be enforced on the backend; this component
 * only explains the outcome, it never gates anything.
 */
export function PermissionDeniedState(): ReactNode {
  const { t } = useTranslation();

  return (
    <StateShell
      tone="warning"
      title={t("common.state.permissionDenied.title")}
      description={t("common.state.permissionDenied.description")}
    />
  );
}
