FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ace-spy-server ./cmd/server

FROM alpine:3.22
RUN adduser -D -u 10001 app
COPY --from=build /out/ace-spy-server /usr/local/bin/ace-spy-server
USER app
ENV PORT=8080
EXPOSE 8080
HEALTHCHECK CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["ace-spy-server"]
