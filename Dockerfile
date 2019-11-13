FROM golang:1.13-alpine as builder
#ENV GOPATH=/go
WORKDIR /

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o  /vault-discovery

FROM alpine:latest

RUN apk add --no-cache --update ca-certificates tzdata python
RUN apk add --no-cache --virtual .build-deps git curl
RUN update-ca-certificates
RUN adduser -D -g '' app

# Gcloud SDK
ARG GCLOUD_SDK_VERSION=270.0.0
RUN curl -fsO https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/google-cloud-sdk-${GCLOUD_SDK_VERSION}-linux-x86_64.tar.gz \
    && tar xzf google-cloud-sdk-${GCLOUD_SDK_VERSION}-linux-x86_64.tar.gz \
    && rm google-cloud-sdk-${GCLOUD_SDK_VERSION}-linux-x86_64.tar.gz \
    && ln -s /lib /lib64

RUN apk del .build-deps

COPY --from=builder /vault-discovery /bin/vault-discovery

USER app
ENV PATH="$PATH:/google-cloud-sdk/bin"
RUN gcloud config set core/disable_usage_reporting true && \
    gcloud config set component_manager/disable_update_check true && \
    gcloud config set core/disable_prompts true && \
    gcloud --version

ENTRYPOINT ["/bin/vault-discovery"]
