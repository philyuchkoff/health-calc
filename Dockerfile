FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o health-calculator

FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata wget && \
    addgroup -S app && adduser -S app -G app
WORKDIR /root/

COPY --from=builder /app/health-calculator .
COPY --from=builder /app/health-config.yaml .

RUN chown -R app:app /root

EXPOSE 8080

USER app

HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=30s \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/ready || exit 1

CMD ["./health-calculator"]
