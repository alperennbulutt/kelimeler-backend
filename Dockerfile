# --- build stage ---
# golang:alpine imajı git + ca-certs içerir; ek "apk add" YOK (CDN'e takılmayı önler).
FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/import ./cmd/import-dictionary

# --- runtime stage ---
# distroless: CA sertifikaları dahil, paket yöneticisi yok → "apk add" gerekmez.
FROM gcr.io/distroless/static-debian12
COPY --from=builder /out/server /server
COPY --from=builder /out/import /import
COPY seed /seed
EXPOSE 8080
ENTRYPOINT ["/server"]
