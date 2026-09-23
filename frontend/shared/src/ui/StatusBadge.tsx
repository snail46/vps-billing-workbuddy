import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { TONE_STYLES, type Tone } from "./tone.js";

/** Props for {@link StatusBadge}. */
export interface StatusBadgeProps {
  /** Semantic tone describing the status. */
  tone: Tone;
  /** i18n key for the status label; never a literal sentence. */
  labelKey: string;
}

/**
 * Compact status indicator.
 *
 * Renders the translated label together with a glyph and a semantic colour, so
 * the status is conveyed by text and shape as well as by hue (docs/12).
 */
export function StatusBadge({ tone, labelKey }: StatusBadgeProps): ReactNode {
  const { t } = useTranslation();
  const style = TONE_STYLES[tone];

  return (
    <span
      data-tone={tone}
      className={`inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-medium ${style.badge}`}
    >
      <span aria-hidden="true">{style.glyph}</span>
      <span>{t(labelKey)}</span>
    </span>
  );
}
