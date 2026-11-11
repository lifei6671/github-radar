import tailwind from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [react(), tailwind()],
  build: {
    rollupOptions: {
      output: {
        // 详情图按需加载，画布渲染器独立缓存。
        manualChunks(id) {
          if (id.includes('/node_modules/zrender/')) return 'chart-renderer'
        },
      },
    },
  },
  server: {
    host: '127.0.0.1',
    proxy: {
      '/api': process.env.RADAR_API_ORIGIN ?? 'http://127.0.0.1:8080',
    },
  },
})
