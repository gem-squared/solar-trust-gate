FROM golang:1.25-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY console/ console/
RUN go build -o solar-trust-gate ./console/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
WORKDIR /app

COPY --from=builder /build/solar-trust-gate .
COPY policies/ policies/
COPY console/demo-assets/ console/demo-assets/
COPY .gem-squared/ce-registry/ .gem-squared/ce-registry/

ENV PORT=8080
EXPOSE 8080
CMD ["./solar-trust-gate"]
