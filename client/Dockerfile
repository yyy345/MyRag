FROM node:22-alpine AS build
WORKDIR /src
COPY package.json package-lock.json ./
COPY client/package.json client/package.json
COPY shared/package.json shared/package.json
COPY web/package.json web/package.json
COPY server/package.json server/package.json
RUN npm ci --workspace client --include-workspace-root=false --ignore-scripts
COPY client/ client/
COPY shared/ shared/
RUN npm run build --workspace client

FROM nginx:1.27-alpine
COPY client/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/client/dist /usr/share/nginx/html
EXPOSE 80
