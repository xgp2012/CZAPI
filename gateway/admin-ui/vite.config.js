import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 构建产物要嵌进 Go 二进制，所以：
//   - base 固定为 /admin/，否则静态资源会去根路径找 404
//   - outDir 指到 Go 侧 embed 的目录
export default defineConfig({
  plugins: [vue()],
  base: '/admin/',
  build: {
    // 产物直接输出到 Go 侧的 embed 包目录。
    // //go:embed 只能引用**同包目录内**的文件，所以不能留在 admin-ui/dist。
    outDir: '../adminui/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1500,
  },
  server: {
    port: 5273,
    proxy: {
      // 开发时把 API 转发给本机网关，避免跨域
      '/api': {
        target: 'http://127.0.0.1:10000',
        changeOrigin: true,
      },
    },
  },
})
