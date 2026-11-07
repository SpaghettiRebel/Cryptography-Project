import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// В тестах привязки Wails заменяются общей подменой src/test/wails.ts.
const wailsMock = fileURLToPath(new URL("./src/test/wails.ts", import.meta.url));

export default defineConfig({
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    alias: [
      { find: /^.*\/wailsjs\/go\/main\/App(\.js)?$/, replacement: wailsMock },
      { find: /^.*\/wailsjs\/runtime\/runtime(\.js)?$/, replacement: wailsMock },
    ],
  },
});
