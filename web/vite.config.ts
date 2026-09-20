import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { mockApi } from "./dev/mock-api";
export default defineConfig({
  plugins: [react(), tailwindcss(), ...(process.env.VITE_MOCK === "1" ? [mockApi()] : [])],
  server: {
    host: "127.0.0.1",
    port: 8080,
    strictPort: true,
    proxy: process.env.VITE_MOCK === "1" ? undefined : { "/api": { target: "http://127.0.0.1:3000", changeOrigin: false } },
  },
});
