# ─── Build stage ─────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS build

WORKDIR /src

# Download dependencies first so this layer is cached unless go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Pure-Go crypto (aes/gcm, scrypt) — no cgo, so build a fully static binary.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/deaddrop .

# ─── Runtime stage ───────────────────────────────────────────────────────────
FROM alpine:3.20

# Run as an unprivileged user.
RUN adduser -D -u 10001 deaddrop
USER deaddrop
WORKDIR /home/deaddrop

COPY --from=build /out/deaddrop /usr/local/bin/deaddrop

# Client A (create) listens here; override the bind address with DEADDROP_ADDR.
ENV DEADDROP_ADDR=:8080
EXPOSE 8080

ENTRYPOINT ["deaddrop"]
