import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      // Forward all /api requests to the local Go backend.
      // Backend default: http://127.0.0.1:8080 (SUPARSHIP_ADDR=:8080).
      // Run `make dev-api` in a separate terminal to start the backend.
      "/api": {
        target: "http://127.0.0.1:8080",
        changeOrigin: true,
      },
    },
  },
});
