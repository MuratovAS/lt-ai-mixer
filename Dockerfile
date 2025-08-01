FROM golang:1-alpine AS builder

RUN mkdir /work
WORKDIR /work

COPY 	go.mod	.
COPY 	go.sum	.
COPY 	*.go	.

RUN go build -o lt-ai-mixer

FROM alpine:latest

COPY --from=builder /work/lt-ai-mixer /app/lt-ai-mixer

EXPOSE 8080
USER nobody
WORKDIR /app

ENTRYPOINT  ["/app/lt-ai-mixer"]
