# --- Build stage ---
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src

# Copy all source (local replace directive requires full whatsmeow source)
COPY . .

# Ensure whatsmeow submodule is present (Portainer/CI may not init submodules)
RUN if [ ! -f whatsmeow/go.mod ]; then \
    rm -rf whatsmeow && \
    git clone --depth 1 https://github.com/tulir/whatsmeow.git whatsmeow; \
    fi

RUN CGO_ENABLED=0 GOTOOLCHAIN=auto go build -trimpath -o /bin/notalk ./cmd/notalk

# --- Runtime stage ---
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S notalk && adduser -S notalk -G notalk

COPY --from=builder /bin/notalk /usr/local/bin/notalk

# Default data directory
RUN mkdir -p /data/db /data/accounts && chown -R notalk:notalk /data

USER notalk
WORKDIR /home/notalk

EXPOSE 3000

ENTRYPOINT ["notalk"]
