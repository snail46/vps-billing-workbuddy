import type { ButtonHTMLAttributes, ReactNode } from "react";
import { useTranslation } from "react-i18next";

/** Visual variants. `danger` is reserved for destructive actions (docs/12). */
export type ButtonVariant = "primary" | "secondary" | "danger" | "ghost";

/** Props for {@link Button}. */
export interface ButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "className"> {
  variant?: ButtonVariant;
  /**
   * Whether the action this button triggers is in flight.
   *
   * A pending button is disabled and visibly busy. docs/AGENTS.md requires
   * visible feedback within 100 ms of a click; for a request that has not yet
   * produced a response, the button owning that feedback is what satisfies it.
   */
  isPending?: boolean;
}

const VARIANT_CLASSES: Readonly<Record<ButtonVariant, string>> = {
  primary:
    "bg-status-info text-white border-transparent hover:brightness-110 focus-visible:outline-status-info",
  secondary:
    "bg-surface text-content border-border-subtle hover:bg-surface-muted focus-visible:outline-status-info",
  danger:
    "bg-status-danger text-white border-transparent hover:brightness-110 focus-visible:outline-status-danger",
  ghost:
    "bg-transparent text-content-muted border-transparent hover:bg-surface-sunken focus-visible:outline-status-info",
};

/**
 * The design system's button.
 *
 * Content is passed as translated text by the caller; the pending label comes
 * from the shared `common.state.loading` key so that no literals are introduced
 * here.
 */
export function Button({
  variant = "secondary",
  isPending = false,
  disabled,
  children,
  type = "button",
  ...rest
}: ButtonProps): ReactNode {
  const { t } = useTranslation();

  return (
    <button
      {...rest}
      type={type}
      disabled={disabled === true || isPending}
      aria-busy={isPending}
      className={`inline-flex items-center gap-2 rounded-md border px-3 py-1.5 text-sm font-medium transition-opacity focus-visible:outline-2 focus-visible:outline-offset-2 disabled:cursor-not-allowed disabled:opacity-60 ${VARIANT_CLASSES[variant]}`}
    >
      {isPending ? (
        <>
          <span
            aria-hidden="true"
            className="h-3 w-3 animate-spin rounded-full border-2 border-current border-t-transparent"
          />
          <span>{t("common.state.loading")}</span>
        </>
      ) : (
        children
      )}
    </button>
  );
}
