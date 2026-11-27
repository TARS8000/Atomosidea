# Build stage（Node.js 24 LTS。版を固定する）
FROM node:24.22.3-alpine AS builder
WORKDIR /app

# Set build arguments
ARG CACHE_BUSTER
ARG VITE_STATIC_SITE_DOMAIN

# Set environment variables from build arguments
ENV VITE_STATIC_SITE_DOMAIN=${VITE_STATIC_SITE_DOMAIN}

COPY package*.json ./
# npm キャッシュのクリアと安定版オプションでのインストール
RUN npm ci --legacy-peer-deps
COPY . .

# Run the build command
RUN npm run build

# Production stage
# 版を固定する（stable-alpine のままだと、手元・サーバーに残った古いイメージが --build でも使われ続ける）。
# 1.30.7 は NGINX2Root（CVE-2026-123007 ほか 4 件）の修正版。上げるときはこの行を変える
FROM nginx:1.30.7-alpine
COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/nginx.conf
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]