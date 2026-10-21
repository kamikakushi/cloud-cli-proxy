import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { TanStackRouterVite } from "@tanstack/router-plugin/vite";
import path from "path";

export default defineConfig({
  plugins: [
    TanStackRouterVite({
      routesDirectory: "./src/routes",
      generatedRouteTree: "./src/routeTree.gen.ts",
    }),
    react(),
    tailwindcss(),
  ],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  server: {
    port: 2568,
    proxy: {
      "/v1": {
        target: process.env.API_URL || "http://127.0.0.1:8080",
        changeOrigin: true,
        ws: true,
      },
    },
  },
  build: {
    reportCompressedSize: false,
    rolldownOptions: {
      output: {
        entryFileNames: "assets/entries/[name]-[hash].js",
        chunkFileNames(chunkInfo) {
          const folder = chunkInfo.name.startsWith("vendor-") ? "vendor" : "chunks";
          return `assets/${folder}/[name]-[hash].js`;
        },
        assetFileNames(assetInfo) {
          const name = assetInfo.names[0] ?? "asset";
          const ext = name.split(".").pop()?.toLowerCase() ?? "";
          if (ext === "css") return "assets/css/[name]-[hash][extname]";
          if (/^(woff|woff2|ttf|otf|eot)$/.test(ext)) return "assets/fonts/[name]-[hash][extname]";
          if (/^(png|jpg|jpeg|gif|svg|webp|avif|mp4|webm)$/.test(ext))
            return "assets/media/[name]-[hash][extname]";
          return "assets/static/[name]-[hash][extname]";
        },
        // Rolldown 原生 codeSplitting 替代 manualChunks（后者在 Rolldown 下
        // 因循环依赖会把库合并进错误 chunk，且 contenthash 随业务代码漂移）。
        // 按 test + priority 稳定分包，vendor chunk 只随库自身内容变化。
        codeSplitting: {
          groups: [
            // 框架核心：react 全家桶，独立分块
            {
              name: "vendor-react",
              test: /[\\/]node_modules[\\/](?:react|react-dom|scheduler)[\\/]/,
              priority: 30,
            },
            // TanStack：router + query
            {
              name: "vendor-tanstack",
              test: /[\\/]node_modules[\\/]@tanstack[\\/]/,
              priority: 25,
            },
            // UI 栈：radix / lucide 图标 / sonner / tailwind 工具
            {
              name: "vendor-ui",
              test: /[\\/]node_modules[\\/](?:radix-ui|@radix-ui|lucide-react|sonner|class-variance-authority|clsx|tailwind-merge)[\\/]/,
              priority: 20,
            },
            // 表单与校验
            {
              name: "vendor-forms",
              test: /[\\/]node_modules[\\/](?:react-hook-form|@hookform|zod)[\\/]/,
              priority: 20,
            },
            // 兜底：其余所有 node_modules
            { name: "vendor-misc", test: /[\\/]node_modules/, priority: 10 },
          ],
        },
      },
    },
  },
});
