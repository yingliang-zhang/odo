import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Vite dev server for the Tauri frontend. Port must match tauri.conf.json
// devUrl; strictPort so Tauri never attaches to the wrong dev server.
export default defineConfig({
  plugins: [react()],
  // Prevent Vite from clearing the terminal output that Tauri also writes to.
  clearScreen: false,
  server: {
    port: 1420,
    strictPort: true,
    watch: {
      // The Rust crate is built by cargo, not Vite.
      ignored: ["**/src-tauri/**"],
    },
  },
});
