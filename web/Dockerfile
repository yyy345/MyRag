FROM node:22-alpine AS build
WORKDIR /src
COPY package.json package-lock.json ./
COPY web/package.json web/package.json
COPY shared/package.json shared/package.json
COPY client/package.json client/package.json
COPY server/package.json server/package.json
RUN npm ci --workspace web --include-workspace-root=false --ignore-scripts
COPY web/ web/
COPY shared/ shared/
RUN npm run build --workspace web

FROM nginx:1.27-alpine
COPY web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/web/dist /usr/share/nginx/html
EXPOSE 80
