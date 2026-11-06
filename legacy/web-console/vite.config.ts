import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The SPA is served by the Go gateway under /console/, so `base` and the built
// asset URLs must be prefixed. The build output is embedded by internal/api via go:embed.
export default defineConfig({
  base: '/console/',
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5273,
    // Optional passthrough for local work against a real gateway.
    proxy: {
      '/console/v1': { target: 'http://127.0.0.1:9999', changeOrigin: true },
      '/admin/v1': { target: 'http://127.0.0.1:9999', changeOrigin: true },
    },
  },
  build: {
    outDir: '../internal/api/console/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1600,
    rollupOptions: {
      output: {
        manualChunks: {
          echarts: ['echarts'],
          element: ['element-plus'],
        },
      },
    },
  },
})
