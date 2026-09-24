/**
 * Semantic tones and the classes that express them.
 *
 * Class strings are written out in full rather than composed at runtime because
 * Tailwind extracts utilities statically: a class assembled from fragments would
 * simply not be generated.
 */
import type { HealthReport } from "../api/types.js";

/** The semantic tones defined by docs/12-DESIGN-SYSTEM.md. */
export const TONES = ["success", "info", "warning", "danger", "neutral"] as const;

/** A semantic tone. */
export type Tone = (typeof TONES)[number];

/** Presentation for one tone. */
export interface ToneStyle {
  /** Classes for a compact badge. */
  badge: string;
  /** Classes for a larger alert surface. */
  alert: string;
  /** A non-colour cue, so state is legible without relying on hue. */
  glyph: string;
}

/**
 * Tone presentation table.
 *
 * Each tone carries a glyph as well as a colour: docs/12 requires that colour is
 * never the only signal, which matters for colour-vision deficiency and for
 * monochrome printing.
 */
export const TONE_STYLES: Readonly<Record<Tone, ToneStyle>> = {
  success: {
    badge: "bg-status-success-surface text-status-success border-status-success-border",
    alert: "bg-status-success-surface border-status-success-border text-status-success",
    glyph: "\u2713",
  },
  info: {
    badge: "bg-status-info-surface text-status-info border-status-info-border",
    alert: "bg-status-info-surface border-status-info-border text-status-info",
    glyph: "\u2139",
  },
  warning: {
    badge: "bg-status-warning-surface text-status-warning border-status-warning-border",
    alert: "bg-status-warning-surface border-status-warning-border text-status-warning",
    glyph: "\u25B2",
  },
  danger: {
    badge: "bg-status-danger-surface text-status-danger border-status-danger-border",
    alert: "bg-status-danger-surface border-status-danger-border text-status-danger",
    glyph: "\u2715",
  },
  neutral: {
    badge: "bg-status-neutral-surface text-status-neutral border-status-neutral-border",
    alert: "bg-status-neutral-surface border-status-neutral-border text-status-neutral",
    glyph: "\u25CB",
  },
};

/**
 * Maps a platform status onto a tone.
 *
 * The mapping lives here rather than in a page so that every surface classifies
 * the same status identically. Later phases extend this module with their own
 * status vocabularies (instance, subscription, operation, order) as the owning
 * phase introduces them; Phase 0 knows only the health vocabulary.
 */
export function healthStatusTone(status: HealthReport["status"]): Tone {
  return status === "up" ? "success" : "danger";
}

/** The operation states the backend machine can be in (docs/05). */
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

/**
 * Tone for one operation state.
 *
 * Waiting states are warnings — neither wrong nor done — and a cancellation is
 * neutral, because the user chose it; reserving danger for genuine failure is
 * what keeps the colour legible.
 */
export function operationStatusTone(status: OperationStatus): Tone {
  switch (status) {
    case "succeeded":
      return "success";
    case "failed":
      return "danger";
    case "waiting_provider":
    case "waiting_resource":
    case "retrying":
      return "warning";
    case "running":
    case "verifying":
      return "info";
    default:
      return "neutral";
  }
}

/** i18n key for an operation status. */
export function operationStatusLabelKey(status: OperationStatus): string {
  return `operation.status.${status}`;
}

/** i18n key for a health status. */
export function healthStatusLabelKey(status: HealthReport["status"]): string {
  return `health.status.${status}`;
}
