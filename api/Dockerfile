FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.mod ./
# TODO: restaurar esta línea cuando exista go.sum.
# COPY go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /medicitas-api .

FROM alpine:3.22

COPY --from=builder /medicitas-api /usr/local/bin/medicitas-api
EXPOSE 8080

ENTRYPOINT ["medicitas-api"]
