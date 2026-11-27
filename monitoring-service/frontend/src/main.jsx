import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App.jsx'
import './index.css'

// 監視ダッシュボードのエントリーポイント。index.html の #root 要素に App をマウントする。
// StrictMode は開発時のみ追加のチェック（effect の二重実行など）を行い、本番ビルドの動作には影響しない。
ReactDOM.createRoot(document.getElementById('root')).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
