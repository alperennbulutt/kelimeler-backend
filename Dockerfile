# --- build stage ---
FROM golang:1.26-alpine AS builder
WORKDIR /app
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/import ./cmd/import-dictionary

# --- runtime stage ---
FROM alpine:3.20
# ca-certificates: HTTPS (Google/Apple doğrulama) | postgresql-client: seed yükleme (psql)
RUN apk add --no-cache ca-certificates postgresql-client
WORKDIR /app
COPY --from=builder /out/server /app/server
COPY --from=builder /out/import /app/import
# Kelime sözlüğü seed'i (production DB'ye bir kez yüklemek için)
COPY seed /app/seed
EXPOSE 8080
ENTRYPOINT ["/app/server"]
