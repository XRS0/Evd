FROM golang:1.21-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git

COPY go.mod ./
RUN go mod download

COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -o server ./cmd/server

FROM alpine:latest

RUN apk add --no-cache ffmpeg

WORKDIR /app

COPY --from=builder /app/server .

RUN mkdir -p videos hls mp4

EXPOSE 8080

CMD ["./server"]
