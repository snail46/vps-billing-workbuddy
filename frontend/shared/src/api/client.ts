import type { ApiEnvelope, ApiErrorBody, HealthReport } from "./types.js";

/**
 * Client-side error codes that have no server counterpart.
 *
 * The server keeps its own vocabulary; these cover failures that happen before
 * a response is ever produced, and are given the same `errors.*` i18n key shape
 * so that rendering a failure needs no special cases.
 */
export const ClientErrorCode = {
  /** The request never produced a response. */
  NetworkError: "NETWORK_ERROR",
  /** A response arrived but did not satisfy the contract. */
  InvalidResponse: "INVALID_RESPONSE",
} as const;

/** Rejection thrown by the API client. */
export class ApiError extends Error {
  /** HTTP status, or 0 when no response was received. */
  readonly status: number;
  /** Stable machine-readable code. */
  readonly code: string;
  /** i18n key describing the failure. */
  readonly messageKey: string;
  /** Structured context, when the server supplied any. */
  readonly details: unknown;
  /**
   * Correlation identifier for this exchange.
   *
   * docs/16 makes the request id the link between a user-visible failure and the
   * server-side log record; surfacing it is what lets a user quote something an
   * operator can find.
   */
  readonly requestId: string;

  constructor(params: {
    status: number;
    code: string;
    messageKey: string;
    requestId: string;
    details?: unknown;
    cause?: unknown;
  }) {
    super(`${params.code} (${params.status})`, { cause: params.cause });
    this.name = "ApiError";
    this.status = params.status;
    this.code = params.code;
    this.messageKey = params.messageKey;
    this.details = params.details;
    this.requestId = params.requestId;
  }
}

/** Configuration for {@link ApiClient}. */
export interface ApiClientOptions {
  /** Base URL of the API, without a trailing slash. */
  baseUrl: string;
  /** Override for tests and non-browser runtimes. */
  fetchImpl?: typeof fetch;
}

/** Per-request options. */
export interface RequestOptions {
  method?: string;
  /** Request body; serialised as JSON when present. */
  body?: unknown;
  /** Additional headers, merged over the defaults. */
  headers?: Record<string, string>;
  /** Caller cancellation. */
  signal?: AbortSignal;
}

const JSON_CONTENT_TYPE = "application/json";

/**
 * Typed HTTP client for the platform API.
 *
 * It owns three responsibilities that must not be re-implemented per call site:
 * unwrapping the docs/08 envelope, converting every failure — including network
 * and malformed-response failures — into an {@link ApiError} carrying an i18n
 * key, and propagating the request id for support and diagnostics.
 */
export class ApiClient {
  private readonly baseUrl: string;
  private readonly fetchImpl: typeof fetch;

  constructor(options: ApiClientOptions) {
    this.baseUrl = options.baseUrl.replace(/\/+$/, "");
    // Bound to globalThis so that a bare `fetch` reference remains callable in
    // environments where it is not a method of the window object.
    this.fetchImpl = options.fetchImpl ?? globalThis.fetch.bind(globalThis);
  }

  /** Performs a request and returns the unwrapped `data` member. */
  async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const response = await this.send(path, options);

    let envelope: ApiEnvelope<T>;
    try {
      envelope = (await response.json()) as ApiEnvelope<T>;
    } catch (cause) {
      throw new ApiError({
        status: response.status,
        code: ClientErrorCode.InvalidResponse,
        messageKey: errorMessageKey(ClientErrorCode.InvalidResponse),
        requestId: response.headers.get("X-Request-ID") ?? "",
        cause,
      });
    }

    if (!isEnvelope(envelope)) {
      throw new ApiError({
        status: response.status,
        code: ClientErrorCode.InvalidResponse,
        messageKey: errorMessageKey(ClientErrorCode.InvalidResponse),
        requestId: response.headers.get("X-Request-ID") ?? "",
      });
    }

    if (!envelope.success) {
      throw this.toApiError(envelope.error, response.status, envelope.request_id);
    }

    return envelope.data;
  }

  /**
   * Performs a request whose response is not an envelope.
   *
   * Used for the operational health endpoints, which answer with plain JSON by
   * design (see docs/17 and the backend health package).
   *
   * The HTTP status is deliberately NOT treated as the outcome. Readiness answers
   * 503 with a body that names the dependency that failed, and discarding that
   * body because the status is not 2xx would throw away the only useful part of
   * the response. A response is therefore rejected only when its body cannot be
   * parsed; interpreting the status is the caller's decision, which is the point
   * of a raw variant.
   */
  async requestRaw<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const response = await this.send(path, options);

    try {
      return (await response.json()) as T;
    } catch (cause) {
      throw new ApiError({
        status: response.status,
        code: ClientErrorCode.InvalidResponse,
        messageKey: errorMessageKey(ClientErrorCode.InvalidResponse),
        requestId: response.headers.get("X-Request-ID") ?? "",
        cause,
      });
    }
  }

  /** Reads the readiness report. */
  fetchHealthReport(): Promise<HealthReport> {
    // A non-2xx status here still carries a usable body: readiness answers 503
    // with the failing dependency named, which is more informative than treating
    // it as a transport failure.
    return this.requestRaw<HealthReport>("/health/ready");
  }

  private async send(path: string, options: RequestOptions): Promise<Response> {
    const headers: Record<string, string> = { Accept: JSON_CONTENT_TYPE, ...options.headers };
    let body: string | undefined;

    if (options.body !== undefined) {
      headers["Content-Type"] = JSON_CONTENT_TYPE;
      body = JSON.stringify(options.body);
    }

    try {
      return await this.fetchImpl(`${this.baseUrl}${path}`, {
        method: options.method ?? "GET",
        headers,
        ...(body === undefined ? {} : { body }),
        // Session cookies are the planned auth mechanism (docs/14), so
        // credentials must travel on cross-origin calls to the API.
        credentials: "include",
        ...(options.signal === undefined ? {} : { signal: options.signal }),
      });
    } catch (cause) {
      // A cancelled request is the caller's own decision, not a failure to
      // report; re-throwing preserves AbortError semantics.
      if (cause instanceof DOMException && cause.name === "AbortError") {
        throw cause;
      }
      throw new ApiError({
        status: 0,
        code: ClientErrorCode.NetworkError,
        messageKey: errorMessageKey(ClientErrorCode.NetworkError),
        requestId: "",
        cause,
      });
    }
  }

  private toApiError(body: ApiErrorBody, status: number, requestId: string): ApiError {
    return new ApiError({
      status,
      code: body.code,
      // The server's key is preferred, but a malformed key would render as
      // nothing at all, so it is validated before being trusted.
      messageKey: isMessageKey(body.message_key)
        ? body.message_key
        : errorMessageKey(body.code),
      requestId,
      details: body.details,
    });
  }
}

/** Maps any error code onto its i18n key, matching the backend convention. */
export function errorMessageKey(code: string): string {
  return `errors.${code.toLowerCase()}`;
}

function isMessageKey(value: unknown): value is string {
  return typeof value === "string" && /^[a-z0-9_]+(\.[a-z0-9_]+)+$/i.test(value);
}

function isEnvelope<T>(value: unknown): value is ApiEnvelope<T> {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const candidate = value as Record<string, unknown>;
  if (typeof candidate["success"] !== "boolean") {
    return false;
  }
  if (candidate["success"]) {
    return "data" in candidate;
  }
  return typeof candidate["error"] === "object" && candidate["error"] !== null;
}
