import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// 开发期把 /api 请求代理到 Go 控制层（:8080），
// 前端代码里一律写相对路径 /api/...，不出现硬编码主机名。
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
    },
  },
})
