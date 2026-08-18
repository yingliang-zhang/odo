import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// Vite dev server for the Tauri frontend. Port must match tauri.conf.json
// devUrl; strictPort so Tauri never attaches to the wrong dev server.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  // Prevent Vite from clearing the terminal output that Tauri also writes to.
  clearScreen: false,
  // Dedupe react/react-dom to prevent two copies when Radix lands (Hermes #527).
  resolve: {
    dedupe: ["react", "react-dom"],
  },
  server: {
    port: 1420,
    strictPort: true,
    watch: {
      // The Rust crate is built by cargo, not Vite.
      ignored: ["**/src-tauri/**"],
    },
  },
});
