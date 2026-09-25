/**
 * The admin client's typed query wrappers and formatting hooks — the same
 * shape the user client uses, so the two consoles read alike.
 */

import { ApiError } from "@vps/shared";
import {
  useMutation as useMutationBase,
  useQuery as useQueryBase,
  type QueryKey,
  type UseMutationOptions,
  type UseMutationResult,
  type UseQueryOptions,
  type UseQueryResult,
} from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { formatDate, formatDateTime, formatMoney, isSupportedLocale, type SupportedLocale } from "@vps/shared";

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

function asLocale(language: string): SupportedLocale {
  return isSupportedLocale(language) ? language : "zh-CN";
}

export function useMoney(): (minor: number, currency: string) => string {
  const { i18n } = useTranslation();
  const locale = asLocale(i18n.language);
  return (minor, currency) => formatMoney(minor, { locale, currency });
}

export function useDate(): (value: string | null | undefined) => string {
  const { i18n } = useTranslation();
  const locale = asLocale(i18n.language);
  return (value) => (value ? formatDateTime(value, { locale }) : "—");
}

export function useDay(): (value: string | null | undefined) => string {
  const { i18n } = useTranslation();
  const locale = asLocale(i18n.language);
  return (value) => (value ? formatDate(value, { locale }) : "—");
}
