FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY main.go .
# Copy go.mod if it existed, but independent build is simpler for this single file
RUN go mod init eval-service && go mod tidy

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux go build -o eval-service main.go

FROM alpine:latest
RUN apk add --no-cache curl
WORKDIR /app
COPY --from=builder /app/eval-service .

EXPOSE 8080
CMD ["./eval-service"]
