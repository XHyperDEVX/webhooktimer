FROM golang:1.22-alpine AS builder

WORKDIR /src

RUN apk add --no-cache ca-certificates

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/webhooktimer .

FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /out/webhooktimer /webhooktimer

EXPOSE 8080
VOLUME ["/data"]

ENV PORT=8080
ENV STATE_PATH=/data/state.json
ENV TZ=UTC

ENTRYPOINT ["/webhooktimer"]
