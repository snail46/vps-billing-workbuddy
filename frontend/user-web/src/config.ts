/**
 * Runtime configuration for the user web client.
 *
 * The API origin is injected at build time so the same source serves local
 * development and the compose environment. Once the reverse proxy in Phase 12
 * fronts both the API and the web clients, this becomes same-origin and the
 * value can be left empty.
 */
export const API_BASE_URL: string = import.meta.env.VITE_API_BASE_URL ?? "http://localhost:8080";
