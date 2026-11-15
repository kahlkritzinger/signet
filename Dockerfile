# Signet on a server (behind Cloudflare Access): see "Run it on a server" in the README.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /signet .

# No shell, no package manager, not root: just the binary.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /signet /signet
ENV SIGNET_DATA=/data SIGNET_LISTEN=0.0.0.0:8080 SIGNET_IDLE=0 SIGNET_LOG=json
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s CMD ["/signet", "health"]
ENTRYPOINT ["/signet"]
CMD ["serve"]
