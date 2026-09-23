/**
 * API contract types.
 *
 * These mirror docs/08-API-CONTRACT.md. The envelope is modelled as a
 * discriminated union on `success` rather than as a single interface with
 * optional members, so a caller cannot read `data` without having established
 * that the request succeeded.
 */

/** Successful response body. */
export interface ApiSuccess<T> {
  success: true;
  data: T;
  request_id: string;
}

/** Failure detail carried by an unsuccessful response. */
export interface ApiErrorBody {
  /** Stable machine-readable code, e.g. `NOT_FOUND`. */
  code: string;
  /**
   * i18n key for the failure, e.g. `errors.not_found`.
   *
   * The backend sends a key rather than a sentence so that no user-visible
   * English or Chinese is baked into an API response (docs/13).
   */
  message_key: string;
  /** Structured, non-localisable context such as field-level validation errors. */
  details?: unknown;
}

/** Unsuccessful response body. */
export interface ApiFailure {
  success: false;
  error: ApiErrorBody;
  request_id: string;
}

/** Any response body emitted by the platform API. */
export type ApiEnvelope<T> = ApiSuccess<T> | ApiFailure;

/**
 * Report returned by `/health/live` and `/health/ready`.
 *
 * These endpoints are operational rather than part of the product API, so they
 * are plain JSON instead of the envelope above.
 */
export interface HealthReport {
  status: "up" | "down";
  version: string;
  commit: string;
  environment: string;
  uptime_seconds: number;
  checks?: HealthCheckReport[];
  time: string;
}

/** Per-dependency outcome inside a readiness report. */
export interface HealthCheckReport {
  name: string;
  status: "up" | "down";
  latency_ms: number;
  error?: string;
}
