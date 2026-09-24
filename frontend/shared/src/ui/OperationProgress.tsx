/**
 * OperationProgress — the shared component for one long operation.
 *
 * Phase 5's surface: an operation's machine state, its derived progress, and
 * the named steps that produced that progress. The component renders what the
 * record says and nothing else — there is no way to hand it a number that did
 * not come from the steps, which is the frontend twin of the backend rule that
 * progress is derived, never reported (docs/07).
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { operationStatusTone, TONE_STYLES } from "./tone.js";

/** The operation states the backend's machine can be in (docs/05). */
export type OperationStatus =
  | "queued"
  | "running"
  | "waiting_provider"
  | "waiting_resource"
  | "verifying"
  | "retrying"
  | "succeeded"
  | "failed"
  | "cancelled";

/** One named stage of the workflow behind an operation. */
export interface OperationStepView {
  /** The step's machine name. */
  key: string;
  /** Position in the workflow, one-based. */
  order: number;
  /** The step's own state. */
  status: "pending" | "running" | "succeeded" | "failed" | "skipped";
  /** How many attempts the step has had. */
  attempt: number;
}

/** The operation data the component renders. */
export interface OperationView {
  /** Machine status. */
  status: OperationStatus;
  /** Progress derived from the steps, 0..100. Rendered as given: the backend
   * already refuses numbers that disagree with the steps. */
  progress: number;
  /** The steps, in order. An empty list means the operation has none yet. */
  steps: readonly OperationStepView[];
  /** The error i18n key suffix, when the machine carries one. */
  errorCode?: string | null;
  /** The failure's message key suffix, when present. */
  errorMessageKey?: string | null;
}

/** Props for {@link OperationProgress}. */
export interface OperationProgressProps {
  operation: OperationView;
}

/**
 * The state vocabulary mapped to the design system's tones, with a non-colour
 * glyph per tone so the state survives monochrome rendering and
 * colour-vision deficiency (docs/12).
 */
const STATUS_LABEL_KEYS: Readonly<Record<OperationStatus, string>> = {
  queued: "operation.status.queued",
  running: "operation.status.running",
  waiting_provider: "operation.status.waiting_provider",
  waiting_resource: "operation.status.waiting_resource",
  verifying: "operation.status.verifying",
  retrying: "operation.status.retrying",
  succeeded: "operation.status.succeeded",
  failed: "operation.status.failed",
  cancelled: "operation.status.cancelled",
};

const STEP_LABEL_KEYS: Readonly<Record<OperationStepView["status"], string>> = {
  pending: "operation.step.pending",
  running: "operation.step.running",
  succeeded: "operation.step.succeeded",
  failed: "operation.step.failed",
  skipped: "operation.step.skipped",
};

/**
 * The live indicator: an operation that is still moving shows a visible pulse,
 * so "running" is a state the user can see without reading the word.
 */
const LIVE_STATUSES: ReadonlySet<OperationStatus> = new Set([
  "queued",
  "running",
  "waiting_provider",
  "waiting_resource",
  "verifying",
  "retrying",
]);

/**
 * Renders one operation's machine state, its derived progress and its steps.
 *
 * The bar's width is the derived progress; the steps below it are the evidence.
 * An aria-live region carries the state, because a user who tabbed away is the
 * exact person a long operation should notify.
 */
export function OperationProgress({ operation }: OperationProgressProps): ReactNode {
  const { t } = useTranslation();
  const tone = operationStatusTone(operation.status);
  const style = TONE_STYLES[tone];
  const live = LIVE_STATUSES.has(operation.status);
  const pct = Math.max(0, Math.min(100, operation.progress));

  return (
    <section aria-live="polite" data-status={operation.status} className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <span
          data-tone={tone}
          className={`inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-medium ${style.badge}`}
        >
          <span aria-hidden="true">{style.glyph}</span>
          <span>{t(STATUS_LABEL_KEYS[operation.status])}</span>
          {live ? <span className="animate-pulse" aria-hidden="true" /> : null}
        </span>
        <span className="text-xs font-medium tabular-nums text-content-muted" aria-hidden="true">
          {pct}%
        </span>
      </div>

      <div
        role="progressbar"
        aria-valuenow={pct}
        aria-valuemin={0}
        aria-valuemax={100}
        className="h-2 w-full overflow-hidden rounded-full bg-status-neutral-surface"
      >
        <div
          className={`h-full rounded-full transition-[width] duration-500 ${style.badge.split(" ")[0]}`}
          style={{ width: `${pct}%` }}
        />
      </div>

      {operation.steps.length > 0 ? (
        <ol className="space-y-1.5">
          {operation.steps.map((step) => {
            const stepTone = STEP_TONES[step.status];
            const stepStyle = TONE_STYLES[stepTone];
            return (
              <li key={step.key} className="flex items-center gap-2 text-xs">
                <span aria-hidden="true" className={`inline-block size-1.5 rounded-full ${stepStyle.badge.split(" ")[0]}`} />
                <span className="font-medium">{step.key}</span>
                <span className="text-content-muted">
                  {t(STEP_LABEL_KEYS[step.status])}
                  {step.attempt > 1 ? ` (${t("operation.step.attempt", { count: step.attempt })})` : ""}
                </span>
              </li>
            );
          })}
        </ol>
      ) : null}

      {operation.errorCode ? (
        <p className="text-xs text-status-danger">
          {t(`error.${operation.errorCode}`)}
        </p>
      ) : null}
    </section>
  );
}

const STEP_TONES: Readonly<Record<OperationStepView["status"], ReturnType<typeof operationStatusTone>>> = {
  pending: "neutral",
  running: "info",
  succeeded: "success",
  failed: "danger",
  skipped: "neutral",
};
