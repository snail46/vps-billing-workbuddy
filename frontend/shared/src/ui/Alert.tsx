import type { ReactNode } from "react";

import { TONE_STYLES, type Tone } from "./tone.js";

/** Props for {@link Alert}. */
export interface AlertProps {
  tone: Tone;
  /** Already-translated message. */
  children: ReactNode;
  /** Optional already-translated heading. */
  title?: ReactNode;
}

/**
 * Inline message surface.
 *
 * The message is passed in already translated rather than as a key, because an
 * alert often combines a translated prefix with details assembled by the caller
 * (a validation summary, a field name), and forcing everything through a single
 * key would push the composition into the i18n files.
 */
export function Alert({ tone, title, children }: AlertProps): ReactNode {
  const style = TONE_STYLES[tone];

  return (
    <div role="alert" className={`rounded-md border px-3 py-2 text-xs ${style.alert}`}>
      <div className="flex items-start gap-2">
        <span aria-hidden="true">{style.glyph}</span>
        <div>
          {title !== undefined && <p className="font-medium">{title}</p>}
          <div className="text-content-muted">{children}</div>
        </div>
      </div>
    </div>
  );
}
