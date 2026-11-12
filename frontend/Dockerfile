FROM node:22-alpine AS build
WORKDIR /build
COPY package*.json ./
RUN npm ci --no-audit --no-fund
COPY tsconfig.json index.html ./
COPY src/ ./src/
RUN npm run build

FROM nginx:1.29-alpine
# Pick up OS security fixes published after the base image was built.
RUN apk upgrade --no-cache && \
    rm /etc/nginx/conf.d/default.conf && \
    mkdir -p /etc/nginx/templates /etc/nginx/conf.d && \
    chown -R nginx:nginx /etc/nginx/conf.d /etc/nginx/templates /usr/share/nginx/html
COPY nginx-main.conf /etc/nginx/nginx.conf
COPY nginx.conf /etc/nginx/templates/default.conf.template
COPY --from=build /build/dist/ /usr/share/nginx/html/
USER nginx
EXPOSE 8080
