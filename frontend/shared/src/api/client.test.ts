import { describe, expect, it, vi } from "vitest";

import { ApiClient, ApiError, ClientErrorCode, errorMessageKey } from "./client.js";

/** Builds a client whose transport is a stub. */
function clientWith(fetchImpl: typeof fetch): ApiClient {
  return new ApiClient({ baseUrl: "https://api.example.com", fetchImpl });
}

function jsonResponse(body: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
}

describe("ApiClient.request", () => {
  it("unwraps the success envelope", async () => {
    const client = clientWith(
      vi.fn(async () =>
        jsonResponse({ success: true, data: { id: "abc" }, request_id: "req-1" }),
      ) as unknown as typeof fetch,
    );

    await expect(client.request<{ id: string }>("/api/v1/thing")).resolves.toEqual({ id: "abc" });
  });

  it("uses the base URL and JSON headers", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ success: true, data: {}, request_id: "r" }));
    const client = clientWith(fetchMock as unknown as typeof fetch);

    await client.request("/api/v1/thing");

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("https://api.example.com/api/v1/thing");
    expect((init.headers as Record<string, string>)["Accept"]).toBe("application/json");
    // Session cookies are the planned auth mechanism, so they must be sent.
    expect(init.credentials).toBe("include");
  });

  it("serialises a body and sets the content type", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ success: true, data: {}, request_id: "r" }));
    const client = clientWith(fetchMock as unknown as typeof fetch);

    await client.request("/api/v1/thing", { method: "POST", body: { quantity: 2 } });

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(init.method).toBe("POST");
    expect(init.body).toBe(JSON.stringify({ quantity: 2 }));
    expect((init.headers as Record<string, string>)["Content-Type"]).toBe("application/json");
  });

  it("converts a failure envelope into an ApiError carrying the request id", async () => {
    const client = clientWith(
      vi.fn(async () =>
        jsonResponse(
          {
            success: false,
            error: { code: "NOT_FOUND", message_key: "errors.not_found" },
            request_id: "req-404",
          },
          404,
        ),
      ) as unknown as typeof fetch,
    );

    const error = await client.request("/api/v1/thing").catch((e: unknown) => e);

    expect(error).toBeInstanceOf(ApiError);
    const apiError = error as ApiError;
    expect(apiError.status).toBe(404);
    expect(apiError.code).toBe("NOT_FOUND");
    expect(apiError.messageKey).toBe("errors.not_found");
    // The request id is what links a user-visible failure to the server log.
    expect(apiError.requestId).toBe("req-404");
  });

  it("derives a message key when the server sends a malformed one", async () => {
    const client = clientWith(
      vi.fn(async () =>
        jsonResponse(
          { success: false, error: { code: "CONFLICT", message_key: "" }, request_id: "r" },
          409,
        ),
      ) as unknown as typeof fetch,
    );

    const error = (await client.request("/api/v1/thing").catch((e: unknown) => e)) as ApiError;

    // An empty key would render as blank text, so the convention is applied
    // locally instead of trusting the payload.
    expect(error.messageKey).toBe("errors.conflict");
  });

  it("reports a non-JSON body as an invalid response", async () => {
    const client = clientWith(
      vi.fn(async () => new Response("<html>gateway error</html>", { status: 502 })) as unknown as typeof fetch,
    );

    const error = (await client.request("/api/v1/thing").catch((e: unknown) => e)) as ApiError;

    expect(error.code).toBe(ClientErrorCode.InvalidResponse);
    expect(error.status).toBe(502);
  });

  it("reports JSON that is not an envelope as an invalid response", async () => {
    const client = clientWith(
      vi.fn(async () => jsonResponse({ unexpected: true })) as unknown as typeof fetch,
    );

    const error = (await client.request("/api/v1/thing").catch((e: unknown) => e)) as ApiError;

    expect(error.code).toBe(ClientErrorCode.InvalidResponse);
  });

  it("reports a transport failure as a network error", async () => {
    const client = clientWith(
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }) as unknown as typeof fetch,
    );

    const error = (await client.request("/api/v1/thing").catch((e: unknown) => e)) as ApiError;

    expect(error).toBeInstanceOf(ApiError);
    expect(error.code).toBe(ClientErrorCode.NetworkError);
    // No response was produced, so there is no status and no request id.
    expect(error.status).toBe(0);
    expect(error.messageKey).toBe("errors.network_error");
  });

  it("propagates caller cancellation instead of masking it as a failure", async () => {
    const abortError = new DOMException("aborted", "AbortError");
    const client = clientWith(
      vi.fn(async () => {
        throw abortError;
      }) as unknown as typeof fetch,
    );

    const error = (await client.request("/api/v1/thing").catch((e: unknown) => e)) as Error;

    // A cancelled request is the caller's decision; turning it into an ApiError
    // would surface a spurious failure message.
    expect(error).toBe(abortError);
    expect(error).not.toBeInstanceOf(ApiError);
  });
});

describe("ApiClient.fetchHealthReport", () => {
  it("returns a 503 readiness body rather than treating it as a transport error", async () => {
    const report = {
      status: "down",
      version: "0.1.0",
      commit: "abc",
      environment: "test",
      uptime_seconds: 3,
      checks: [{ name: "postgres", status: "down", latency_ms: 12, error: "connection refused" }],
      time: "2026-09-23T00:00:00Z",
    };
    const client = clientWith(
      vi.fn(async () => jsonResponse(report, 503)) as unknown as typeof fetch,
    );

    // A not-ready platform still answers with a usable body naming the failing
    // dependency, which is more informative than a generic failure.
    await expect(client.fetchHealthReport()).resolves.toMatchObject({ status: "down" });
  });

  it("still rejects a genuinely failed probe", async () => {
    const client = clientWith(
      vi.fn(async () => new Response("", { status: 500 })) as unknown as typeof fetch,
    );

    await expect(client.fetchHealthReport()).rejects.toBeInstanceOf(ApiError);
  });
});

describe("errorMessageKey", () => {
  it("follows the backend convention", () => {
    expect(errorMessageKey("NOT_FOUND")).toBe("errors.not_found");
    expect(errorMessageKey("REQUEST_TIMEOUT")).toBe("errors.request_timeout");
  });
});
