# One image recipe for every service: docker build --build-arg CMD=order/cmd/order .
FROM golang:1.24-alpine3.21 AS build
ARG CMD
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor vendor
COPY account account
COPY catalog catalog
COPY order order
COPY graphql graphql
COPY backdraft backdraft
COPY internal internal
RUN CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/app ./${CMD}

FROM alpine:3.21
RUN adduser -D -H app
USER app
COPY --from=build /out/app /usr/bin/app
EXPOSE 8080
ENTRYPOINT ["app"]
