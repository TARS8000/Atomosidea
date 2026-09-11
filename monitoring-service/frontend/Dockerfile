# ベースイメージ
FROM node:20-alpine AS builder

# 作業ディレクトリを設定
WORKDIR /app

# package.json と package-lock.json をコピー
COPY package*.json ./

# 依存関係をインストール
RUN npm install

# ソースコードをコピー
COPY . .

# Viteアプリケーションをビルド
RUN npm run build

# ビルド成果物はホストのボリュームに書き出されるため、最終イメージへのコピーは不要
# FROM nginx:alpine
# COPY --from=builder /app/dist /usr/share/nginx/html
