import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// VITE_DEBUG=true  — enable verbose proxy + API request logs in the browser console.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const debugProxy = env.VITE_DEBUG === "true";

  return {
    plugins: [react(), tailwindcss()],
    define: {
      // Expose VITE_DEBUG to the browser bundle so api.ts can read it.
      __VITE_DEBUG__: JSON.stringify(debugProxy),
    },
    server: {
      proxy: {
        // Forward all /api requests to the local Go backend.
        // Backend default: http://127.0.0.1:8080 (SUPARSHIP_ADDR=:8080).
        // Run `make dev-api` in a separate terminal to start the backend.
        "/api": {
          target: "http://127.0.0.1:8080",
          changeOrigin: true,
          configure(proxy) {
            if (!debugProxy) return;
            proxy.on("proxyReq", (_proxyReq, req) => {
              console.log(`[proxy] --> ${req.method} ${req.url}`);
            });
            proxy.on("proxyRes", (proxyRes, req) => {
              console.log(
                `[proxy] <-- ${proxyRes.statusCode} ${req.method} ${req.url}`,
              );
            });
            proxy.on("error", (err, req) => {
              console.error(`[proxy] error ${req.method} ${req.url}:`, err.message);
            });
          },
        },
      },
    },
  };
});
