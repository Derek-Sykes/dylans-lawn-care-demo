FROM golang:1.27.1-alpine AS build
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

FROM build AS test
RUN --mount=type=cache,target=/root/.cache/go-build go test -race -count=1 ./...

FROM build AS compile
ARG REVISION=local
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=1 go build -trimpath -ldflags="-s -w -X main.revision=${REVISION}" -o /booking .

FROM alpine:3.24 AS runtime
ARG REVISION=local
LABEL org.opencontainers.image.title="Dylan's Lawn Care booking service"
LABEL org.opencontainers.image.source="https://github.com/Derek-Sykes/dylans-lawn-care-demo"
LABEL org.opencontainers.image.revision=$REVISION
RUN apk add --no-cache ca-certificates tzdata wget && addgroup -g 10001 booking && adduser -D -u 10001 -G booking booking && mkdir /data && chown booking:booking /data
COPY --from=compile /booking /usr/local/bin/booking
USER booking
ENV DATA_DIR=/data
EXPOSE 8081 8082
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=6 CMD wget -q -O /dev/null http://127.0.0.1:8081/healthz && wget -q -O /dev/null http://127.0.0.1:8082/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/booking"]
