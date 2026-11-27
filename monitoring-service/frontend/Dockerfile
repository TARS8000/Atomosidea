# ベースイメージ
FROM node:24.22.3-alpine AS builder

# 作業ディレクトリを設定
WORKDIR /app

# package.json と package-lock.json をコピー（lockfile どおりに入れる）
COPY package*.json ./

# 依存関係をインストール
RUN npm ci

# ソースコードをコピー
COPY . .

# Viteアプリケーションをビルド
RUN npm run build

# ビルド成果物（dist）はホストのボリュームに書き出され、mon-nginx が配信する（最終イメージへのコピーは不要）
