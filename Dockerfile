FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/codex-telegram-approve .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 bridge
COPY --from=build /out/codex-telegram-approve /usr/local/bin/codex-telegram-approve
USER bridge
EXPOSE 8080
ENTRYPOINT ["codex-telegram-approve"]
CMD ["serve"]
