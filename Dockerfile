# --- build stage ---
# golang:alpine imajı git + ca-certs içerir; ek "apk add" YOK (CDN'e takılmayı önler).
FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Sürüm bilgisi binary'ye gömülür ve SENTRY_RELEASE boşsa Sentry sürümü olur.
# Coolify, "Include Source Commit in Build" açıkken SOURCE_COMMIT'i build arg
# olarak verir; verilmezse sürüm boş kalır (SENTRY_RELEASE elle girilebilir).
ARG SOURCE_COMMIT=""
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X main.version=${SOURCE_COMMIT}" \
      -o /out/server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/import ./cmd/import-dictionary
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/cleanup-guests ./cmd/cleanup-guests

# --- import aracı (isteğe bağlı) ---
# Production'da gerekmez (sözlük seed/ ile yüklenir); yalnızca açıkça istenince
# derlenir:  docker build --target import .   ya da   docker compose run --rm import
FROM gcr.io/distroless/static-debian12:nonroot AS import
WORKDIR /
COPY --from=builder /out/import /import
USER nonroot:nonroot
ENTRYPOINT ["/import"]

# --- runtime stage (varsayılan hedef: son aşama) ---
# distroless: CA sertifikaları dahil, paket yöneticisi ve kabuk yok.
# :nonroot etiketi ve USER satırı süreci root olmayan kullanıcıyla (uid 65532)
# çalıştırır; 8080 ayrıcalıklı olmayan bir port olduğu için yetki gerekmez.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/server /server
# Elle çalıştırılan terk edilmiş misafir temizliği; kabuk olmadığı için
# container'ın ortamıyla doğrudan çalıştırılır (bkz. DEPLOYMENT.md):
#   docker exec <container> /cleanup-guests            # yalnızca rapor
#   docker exec <container> /cleanup-guests --apply    # sil
COPY --from=builder /out/cleanup-guests /cleanup-guests
COPY seed /seed
USER nonroot:nonroot
EXPOSE 8080
# Kabuk/curl olmadığından sağlık kontrolü binary'nin kendisiyle yapılır;
# /api/v1/health veritabanına ulaşılamazsa 503 döner ve kontrol başarısız olur.
HEALTHCHECK --interval=15s --timeout=6s --start-period=60s --retries=3 \
  CMD ["/server", "-healthcheck"]
ENTRYPOINT ["/server"]
