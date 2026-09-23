/**
 * @vps/shared — the single source of truth for everything both web clients need.
 *
 * docs/10 and docs/11 describe two applications with different audiences but a
 * large shared surface: i18n, the API contract, the design system and the page
 * states. Keeping that surface here is what stops the two clients from drifting
 * apart, and it is why the shared package contains no privileged code
 * (docs/14 requires the user and admin surfaces to stay isolated).
 */

export type {
  ApiEnvelope,
  ApiErrorBody,
  ApiFailure,
  ApiSuccess,
  HealthCheckReport,
  HealthReport,
} from "./api/types.js";

export {
  ApiClient,
  ApiError,
  ClientErrorCode,
  errorMessageKey,
  type ApiClientOptions,
  type RequestOptions,
} from "./api/client.js";

export {
  createI18n,
  DEFAULT_LOCALE,
  DEFAULT_NAMESPACE,
  detectLocale,
  failureTone,
  isSupportedLocale,
  LOCALE_STORAGE_KEY,
  resources,
  SUPPORTED_LOCALES,
  type SupportedLocale,
} from "./i18n/index.js";

export { bootstrapI18n, readStoredLocale } from "./i18n/bootstrap.js";

export { zhCN } from "./i18n/locales/zh-CN.js";
export { enUS } from "./i18n/locales/en-US.js";

export {
  currencyExponent,
  formatMoney,
  scaleMinorUnits,
  type FormatMoneyOptions,
} from "./format/money.js";

export {
  formatDate,
  formatDateTime,
  splitDuration,
  type DateInput,
  type FormatDateTimeOptions,
} from "./format/datetime.js";

export {
  healthStatusLabelKey,
  healthStatusTone,
  TONES,
  TONE_STYLES,
  type Tone,
  type ToneStyle,
} from "./ui/tone.js";

export { Alert, type AlertProps } from "./ui/Alert.js";
export { Button, type ButtonProps, type ButtonVariant } from "./ui/Button.js";
export { Card, type CardProps } from "./ui/Card.js";
export { LocaleSwitcher } from "./ui/LocaleSwitcher.js";
export { StatusBadge, type StatusBadgeProps } from "./ui/StatusBadge.js";
export {
  EmptyState,
  ErrorState,
  LoadingState,
  PartialErrorNotice,
  PermissionDeniedState,
  type ErrorStateProps,
} from "./ui/states.js";
