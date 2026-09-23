import { fileURLToPath } from "node:url";

import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

/**
 * The shared package is resolved to its TypeScript source rather than to a build
 * output. There is then no build ordering to get wrong and no stale `dist` to
 * debug: whatever the apps compile is exactly what is in `shared/src`.
 */
const sharedEntry = fileURLToPath(new URL("../shared/src/index.ts", import.meta.url));

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@vps/shared": sharedEntry,
    },
    // Both applications and the shared package declare React; resolving a single
    // copy prevents the "invalid hook call" failure caused by two React instances.
    dedupe: ["react", "react-dom"],
  },
  server: {
    port: 3000,
    strictPort: true,
  },
  build: {
    outDir: "dist",
    sourcemap: true,
  },
});
