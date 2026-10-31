import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8081",
        ws: true,
        changeOrigin: false,
      },
      "/healthz": "http://127.0.0.1:8081",
      "/readyz": "http://127.0.0.1:8081",
    },
  },
});
