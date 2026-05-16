FROM golang:1.22-alpine AS build
WORKDIR /src
ENV GOPROXY=https://proxy.golang.org,direct
COPY go.mod go.sum ./
COPY packages/ packages/
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go build -o /out/service ./services/notification-error-service
FROM alpine:3.20
COPY --from=build /out/service /service
EXPOSE 8080
ENTRYPOINT ["/service"]
