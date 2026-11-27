import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vitejs.dev/config/
export default defineConfig({
  // React（JSX 変換・Fast Refresh）用のプラグインを使う。
  plugins: [react()],
  // `base` オプションは削除した。クライアントサイドルーティングを使う多くの SPA では、
  // 既定値の '/' が正しい設定である。
  // 開発サーバーのポート。ここには /api のプロキシ設定が無い。
  // Docker 構成では、ビルド結果を配信する nginx（frontend/nginx.conf）が /api を各サービスへ中継する。
  server: {
    port: 3000,
  }
})
