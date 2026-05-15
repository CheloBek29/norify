FROM golang:1.22-alpine AS build
ENV GOPROXY=https://goproxy.cn,direct
WORKDIR /src
COPY go.mod go.sum ./
COPY packages/ packages/
RUN go mod download
COPY . .
RUN go build -mod=vendor -o /out/service ./services/sender-worker
FROM alpine:3.20
COPY --from=build /out/service /service
EXPOSE 8080
ENTRYPOINT ["/service"]
