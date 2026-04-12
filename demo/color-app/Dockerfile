FROM golang:1.22-alpine AS builder
ARG VERSION=dev
ARG COLOR=blue
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o /color-app .

FROM scratch
COPY --from=builder /color-app /color-app
ENV COLOR=${COLOR}
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/color-app"]
