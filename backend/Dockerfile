FROM golang:1.26-bookworm AS build
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN for command in api migrate outbox results storage-cleanup scheduler; do CGO_ENABLED=0 go build -trimpath -o /out/$command ./cmd/$command; done

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app/backend
COPY --from=build /out/ /usr/local/bin/
COPY server/prisma/migrations/ /app/server/prisma/migrations/
USER 65532:65532
EXPOSE 8080
CMD ["api"]
