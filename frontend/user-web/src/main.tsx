import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiClient, bootstrapI18n } from "@vps/shared";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import { BrowserRouter } from "react-router-dom";

import { App } from "./app.js";
import { SessionProvider } from "./session.js";
import { API_BASE_URL } from "./config.js";
import "./styles.css";

const i18n = bootstrapI18n();

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Data is considered fresh briefly so that navigating between screens does
      // not refetch on every mount.
      staleTime: 15_000,
      // One retry covers a transient blip without hiding a real outage behind
      // repeated attempts.
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});

const apiClient = new ApiClient({ baseUrl: API_BASE_URL });

const container = document.getElementById("root");
if (container === null) {
  // Failing loudly is correct: rendering nothing would look like an empty page
  // rather than a broken build.
  throw new Error("index.html must provide a #root element");
}

createRoot(container).render(
  <StrictMode>
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <SessionProvider apiClient={apiClient}>
          <BrowserRouter>
            <App apiClient={apiClient} />
          </BrowserRouter>
        </SessionProvider>
      </QueryClientProvider>
    </I18nextProvider>
  </StrictMode>,
);
