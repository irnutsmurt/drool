import { defineConfig, mergeConfig } from "vitest/config";
import viteConfig from "./vite.config";

// Unit tests only; the browser tests in e2e/ run with node --test against a
// live controller (see e2e/lib.mjs).
export default mergeConfig(viteConfig, defineConfig({ test: { include: ["src/**/*.test.ts"] } }));
