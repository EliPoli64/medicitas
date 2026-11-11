FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /medicitas-api .

FROM alpine:3.22

# Zonas IANA (America/Costa_Rica, etc.) para RF20/RF21.
RUN apk add --no-cache tzdata

COPY --from=builder /medicitas-api /usr/local/bin/medicitas-api
EXPOSE 8080

ENTRYPOINT ["medicitas-api"]
