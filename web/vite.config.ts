import { defineConfig } from "vite"
import { tanstackStart } from "@tanstack/react-start/plugin/vite"
import viteReact from "@vitejs/plugin-react"
import tailwindcss from "@tailwindcss/vite"

const apiProxy = {
  "/api": {
    target: "http://127.0.0.1:8787",
    changeOrigin: true,
  },
  "/proxy": {
    target: "http://127.0.0.1:8787",
    changeOrigin: true,
  },
}

const config = defineConfig({
  resolve: { tsconfigPaths: true },
  server: {
    host: "127.0.0.1",
    port: 3000,
    proxy: apiProxy,
  },
  preview: {
    host: "127.0.0.1",
    port: 3000,
    proxy: apiProxy,
  },
  plugins: [tailwindcss(), tanstackStart(), viteReact()],
})

export default config
