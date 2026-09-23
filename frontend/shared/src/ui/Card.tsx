import type { ReactNode } from "react";

/** Props for {@link Card}. */
export interface CardProps {
  /** Optional translated heading. */
  title?: ReactNode;
  /** Optional translated supporting text. */
  description?: ReactNode;
  /** Rendered in the card's top-right corner, typically an action. */
  action?: ReactNode;
  children: ReactNode;
}

/** A bordered content surface. */
export function Card({ title, description, action, children }: CardProps): ReactNode {
  return (
    <section className="rounded-lg border border-border-subtle bg-surface shadow-sm">
      {(title !== undefined || action !== undefined) && (
        <header className="flex items-start justify-between gap-4 border-b border-border-subtle px-4 py-3">
          <div>
            {title !== undefined && (
              <h2 className="text-sm font-semibold text-content">{title}</h2>
            )}
            {description !== undefined && (
              <p className="mt-0.5 text-xs text-content-subtle">{description}</p>
            )}
          </div>
          {action}
        </header>
      )}
      <div className="px-4 py-3">{children}</div>
    </section>
  );
}
