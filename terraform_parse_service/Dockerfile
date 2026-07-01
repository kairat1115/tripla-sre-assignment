FROM golang:1.25-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o server ./cmd/server


FROM scratch

COPY --from=builder /build/server /server
COPY --from=builder /build/configs /configs
COPY --from=builder /build/templates /templates

ENV CONFIG_PATH=/configs/config.yaml

EXPOSE 8080

ENTRYPOINT ["/server"]
