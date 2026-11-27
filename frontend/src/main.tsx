import React from 'react';
import ReactDOM from 'react-dom/client';
import App from './App.tsx';
import './index.css';

// axios はこのファイルではグローバル設定しないため、import を削除している。
// axios の呼び出しはすべて各コンポーネント（例: HomePage）内で行う。
// （Authorization ヘッダーを付与するインターセプターは context/AuthContext.tsx で登録している）

// #root 要素に App を描画する。StrictMode では開発時に useEffect などが 2 回実行される。
ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
