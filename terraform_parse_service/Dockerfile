FROM golang:1.26-alpine AS builder

WORKDIR /build

ARG OTELC_VERSION=v1.0.1

COPY go.mod go.sum ./
RUN go mod download
RUN GOBIN=/usr/local/bin go install go.opentelemetry.io/otelc/tool/cmd/otelc@${OTELC_VERSION} \
    && go clean -cache

COPY . .
RUN CGO_ENABLED=0 otelc go build -trimpath -o server ./cmd/server


FROM scratch

COPY --from=builder /build/server /server
COPY --from=builder /build/configs /configs
COPY --from=builder /build/templates /templates

ENV CONFIG_PATH=/configs/config.yaml

EXPOSE 8080 9091

ENTRYPOINT ["/server"]
