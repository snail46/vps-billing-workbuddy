import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiClient, bootstrapI18n } from "@vps/shared";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { I18nextProvider } from "react-i18next";

import { App } from "./app.js";
import { API_BASE_URL } from "./config.js";
import "./styles.css";

const i18n = bootstrapI18n("app.adminName");

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // An operations console is read continuously; a shorter stale window than
      // the user client keeps the view closer to reality.
      staleTime: 10_000,
      // Operators need to see a failure, not have it hidden behind retries.
      retry: 1,
      // Returning to the tab should re-check: an operator alt-tabbing back after
      // an incident must not be looking at stale health.
      refetchOnWindowFocus: true,
    },
  },
});

const apiClient = new ApiClient({ baseUrl: API_BASE_URL });

const container = document.getElementById("root");
if (container === null) {
  throw new Error("index.html must provide a #root element");
}

createRoot(container).render(
  <StrictMode>
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <App apiClient={apiClient} />
      </QueryClientProvider>
    </I18nextProvider>
  </StrictMode>,
);
