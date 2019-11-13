FROM golang:1.13-alpine as builder
ENV GOPATH=/go
WORKDIR /

COPY . .
RUN apk --no-cache --update add git ca-certificates tzdata
RUN update-ca-certificates
RUN adduser -D -g '' app

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o vault-discovery

FROM scratch

COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /go/vault-discovery /bin/vault-discovery

USER app

ENTRYPOINT ["/bin/vault-discovery"]
