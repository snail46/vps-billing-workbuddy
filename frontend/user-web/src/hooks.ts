/**
 * Query and mutation helpers, plus the operation stream.
 *
 * The two hooks exist so a page's server state reads the same everywhere:
 * the query key is the path, an {@link ApiError} is the failure, and the
 * error page renders the message key and request id the error carries.
 */

import { ApiError, operationStatusLabelKey, type ApiClient, type OperationView, type OperationStepView } from "@vps/shared";
import {
  useMutation as useMutationBase,
  useQuery as useQueryBase,
  type QueryKey,
  type UseMutationOptions,
  type UseMutationResult,
  type UseQueryOptions,
  type UseQueryResult,
} from "@tanstack/react-query";
import { useEffect, useState } from "react";

import type { Operation, OperationDetail } from "./api/types.js";

// The typed wrappers exist so every page's failure is an ApiError by default:
// a wrong assertion about what went wrong is then a type error, not a page
// that renders the wrong sentence.

export function useQuery<TQueryFnData, TError = ApiError, TData = TQueryFnData, TQueryKey extends QueryKey = QueryKey>(
  options: UseQueryOptions<TQueryFnData, TError, TData, TQueryKey>,
): UseQueryResult<TData, TError> {
  return useQueryBase<TQueryFnData, TError, TData, TQueryKey>(options);
}

export function useMutation<TData, TError = ApiError, TVariables = void>(
  options: UseMutationOptions<TData, TError, TVariables>,
): UseMutationResult<TData, TError, TVariables> {
  return useMutationBase<TData, TError, TVariables>(options);
}

/** Maps the operation detail payload onto what the shared progress renders. */
export function operationToView(detail: OperationDetail): OperationView {
  const steps: OperationStepView[] = detail.steps.map((step) => ({
    key: step.key,
    order: step.order,
    status: step.status as OperationStepView["status"],
    attempt: step.attempt,
  }));
  return {
    status: detail.operation.status as OperationView["status"],
    progress: detail.operation.progress,
    steps,
    errorCode: detail.operation.error_code,
    errorMessageKey: detail.operation.error_code !== null ? `errors.${detail.operation.error_code.toLowerCase()}` : null,
  };
}

/** Maps a bare operation (a stream push) onto the view, steps unknown. */
export function operationToViewWithoutSteps(operation: Operation): OperationView {
  return {
    status: operation.status as OperationView["status"],
    progress: operation.progress,
    steps: [],
    errorCode: operation.error_code,
    errorMessageKey: operation.error_code !== null ? `errors.${operation.error_code.toLowerCase()}` : null,
  };
}

void operationStatusLabelKey;

export function useApiQuery<T>(
  apiClient: ApiClient,
  path: string,
  queryFn: (client: ApiClient) => Promise<T>,
): UseQueryResult<T, ApiError> {
  return useQuery<T, ApiError>({ queryKey: [path], queryFn: () => queryFn(apiClient) });
}

export function useApiMutation<TInput, TResult>(
  mutationFn: (input: TInput) => Promise<TResult>,
): UseMutationResult<TResult, ApiError, TInput> {
  return useMutation<TResult, ApiError, TInput>({ mutationFn });
}

/**
 * Watches one operation.
 *
 * The live stream is the server's `/events` SSE; a refresh re-derives the
 * state from the operation row rather than remembering anything. Where the
 * stream cannot connect — a proxy that buffers, a network that stalls — the
 * query falls back to a two-second poll of the same row, so the progress a
 * customer sees is the record either way.
 */
export function useOperationStream(
  apiClient: ApiClient,
  apiBaseUrl: string,
  operationID: string | null,
): { operation: Operation | null; loading: boolean; error: string | null } {
  const [live, setLive] = useState<Operation | null>(null);
  const [streamFailed, setStreamFailed] = useState(false);

  const query = useQuery<Operation, ApiError>({
    queryKey: ["operation", operationID],
    enabled: operationID !== null,
    queryFn: () => fetchOperation(apiClient, operationID ?? ""),
    // The poll is the fallback and the initial answer; while the stream is
    // healthy the pushes arrive faster than the poll matters.
    refetchInterval: streamFailed ? 2_000 : 5_000,
    retry: false,
  });

  useEffect(() => {
    if (operationID === null || streamFailed) {
      return;
    }
    const url = `${apiBaseUrl}/api/v1/events?operation_id=${encodeURIComponent(operationID)}`;
    let source: EventSource | null = null;
    try {
      source = new EventSource(url, { withCredentials: true });
    } catch {
      // Defer the fallback switch: a state write straight out of an effect
      // would cascade renders.
      globalThis.setTimeout(() => setStreamFailed(true), 0);
      return;
    }
    const timer = globalThis.setTimeout(() => {
      // A stream that has not pushed its first keepalive within two seconds is
      // treated as unavailable; polling takes over.
      if (live === null) {
        setStreamFailed(true);
        source?.close();
      }
    }, 2_000);
    source.onmessage = (event) => {
      globalThis.clearTimeout(timer);
      try {
        const envelope = JSON.parse(event.data) as { data?: { operation?: Operation } };
        if (envelope.data?.operation !== undefined) {
          setLive(envelope.data.operation);
        }
      } catch {
        // A malformed frame is skipped; the poll still bounds staleness.
      }
    };
    source.onerror = () => {
      setStreamFailed(true);
      source?.close();
    };
    return () => {
      globalThis.clearTimeout(timer);
      source?.close();
    };
    // `live` is deliberately excluded: the timer's job is only to detect the
    // very first sign of life.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apiBaseUrl, operationID, streamFailed]);

  if (operationID === null) {
    return { operation: null, loading: false, error: null };
  }

  const operation = live ?? query.data ?? null;
  return {
    operation,
    loading: query.isPending && live === null,
    error: query.error?.messageKey ?? null,
  };
}

function fetchOperation(apiClient: ApiClient, id: string): Promise<Operation> {
  return apiClient.request<{ operation: Operation }>(`/api/v1/operations/${id}`).then((d) => d.operation);
}
