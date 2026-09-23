/**
 * en-US resources.
 *
 * Must expose exactly the same key set as zh-CN; a parity test enforces that, so
 * a missing translation fails CI instead of silently falling back to the other
 * language.
 */
import type { TranslationShape } from "../shape.js";
import type { zhCN } from "./zh-CN.js";

export const enUS: TranslationShape<typeof zhCN> = {
  app: {
    name: "VPS Billing Platform",
    adminName: "VPS Billing Platform · Admin",
    environment: "Environment",
  },
  common: {
    actions: {
      retry: "Retry",
      refresh: "Refresh",
    },
    state: {
      loading: "Loading",
      empty: {
        title: "Nothing here yet",
        description: "There is no content to display.",
      },
      error: {
        title: "Could not load",
        description:
          "Please try again. If it keeps failing, quote the request ID to support.",
      },
      partialError: {
        title: "Some data is unavailable",
        description: "The page loaded, but part of its content could not be fetched.",
      },
      permissionDenied: {
        title: "No access",
        description: "Your account does not have permission to view this.",
      },
    },
    requestId: "Request ID",
    unavailable: "Unavailable",
  },
  errors: {
    internal_error: "Something went wrong on our side. Please try again.",
    validation_failed: "The submitted data is not valid. Please check and retry.",
    unauthorized: "Your session has expired. Please sign in again.",
    forbidden: "You do not have permission to perform this action.",
    not_found: "The requested resource does not exist.",
    method_not_allowed: "That request method is not supported.",
    conflict: "This conflicts with the current state. Please refresh and retry.",
    rate_limited: "Too many requests. Please try again shortly.",
    request_timeout: "The request took too long. Please try again.",
    service_unavailable: "The service is temporarily unavailable. Please try again.",
    network_error: "Could not reach the server. Check your connection and retry.",
    invalid_response: "The server returned data we could not understand.",
    unknown: "An unexpected error occurred. Please try again.",
    // The authentication failures. They are separate from the transport codes above
    // because they say something more specific: `unauthorized` means the session is gone
    // and the user should sign in again, while `invalid_credentials` means the password
    // just entered does not match. Showing the first for the second would send someone
    // looking for a session they never had.
    invalid_credentials: "That email address or password is not correct.",
    account_suspended: "This account has been suspended. Please contact support.",
    email_taken: "That email address is already registered. Sign in instead, or use another.",
    invalid_email: "Enter a valid email address.",
    invalid_password: "That password does not meet the requirements. Please choose a longer one.",
    invalid_locale: "That language is not supported.",
    // The commerce failures. They say what went wrong rather than only that something
    // did, because a payment that silently failed is worse than one that says why.
    signature_invalid: "The callback signature is invalid and it has been refused.",
    malformed_callback: "The callback could not be parsed.",
    order_not_payable: "This order can no longer be paid. Please refresh to see its state.",
    payment_already_started: "A payment has already been started for this order.",
    payment_amount_mismatch: "The callback amount disagrees with the order, so it has not been recorded.",
    payment_currency_mismatch: "The callback currency disagrees with the order, so it has not been recorded.",
    unsupported_notification: "This kind of callback cannot be handled yet.",
    invalid_plan: "That plan does not exist or is no longer on sale.",
    plan_not_purchaseable: "That plan is not available to buy right now.",
    mixed_currencies: "An order can only contain one currency.",
    empty_order: "Choose at least one item.",
  },
  health: {
    title: "Platform status",
    subtitle: "Live availability of the foundational dependencies.",
    status: {
      up: "Operational",
      down: "Degraded",
    },
    field: {
      version: "Version",
      commit: "Commit",
      environment: "Environment",
      uptime: "Uptime",
      checkedAt: "Checked at",
    },
    checks: {
      title: "Dependency checks",
      empty: "No dependency checks are registered yet.",
      latency: "{{ms}} ms",
      column: {
        name: "Dependency",
        status: "Status",
        latency: "Latency",
        diagnostic: "Diagnostic",
      },
    },
    uptime: {
      seconds: "{{count}} seconds",
      minutes: "{{count}} minutes",
      hours: "{{count}} hours",
      days: "{{count}} days",
    },
  },
  foundation: {
    title: "Phase 0 foundation",
    description:
      "This page exercises the foundation: API calls, error handling, both locales and the design tokens. Business pages arrive in later phases.",
    scope: "Current scope",
    scopeItems: {
      api: "API connectivity and the shared response envelope",
      i18n: "zh-CN / en-US resources with a language switch",
      states: "loading / empty / error / partial_error / permission_denied",
      tokens: "Design-system semantic colours and status badges",
    },
  },
  locale: {
    label: "Language",
    "zh-CN": "简体中文",
    "en-US": "English",
  },
};
