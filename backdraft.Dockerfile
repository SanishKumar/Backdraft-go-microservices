# The Backdraft control plane with its web UI.
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web ./
RUN npm run build

FROM golang:1.24-alpine3.21 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor vendor
COPY account account
COPY catalog catalog
COPY order order
COPY graphql graphql
COPY backdraft backdraft
COPY internal internal
COPY cmd cmd
COPY web/*.go web/
RUN CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/backdraft ./cmd/backdraft

FROM alpine:3.21
RUN adduser -D -H app
RUN mkdir -p /data/reviews && chown -R app:app /data
USER app
COPY --from=build /out/backdraft /usr/bin/backdraft
COPY --from=web /web/dist /srv/ui
EXPOSE 7070 7071
ENTRYPOINT ["backdraft", "serve", "--ui", "/srv/ui", "--data", "/data/reviews"]
