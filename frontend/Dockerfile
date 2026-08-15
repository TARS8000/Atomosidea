# Build stage
FROM node:18-alpine AS builder
WORKDIR /app

# Set build arguments
ARG CACHE_BUSTER
ARG VITE_STATIC_SITE_DOMAIN

# Set environment variables from build arguments
ENV VITE_STATIC_SITE_DOMAIN=${VITE_STATIC_SITE_DOMAIN}

COPY package*.json ./
# Explicitly remove node_modules before npm install to prevent ETXTBSY errors
RUN rm -rf node_modules && npm install
COPY . .

# Run the build command
RUN npm run build

# Production stage
FROM nginx:stable-alpine
COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]