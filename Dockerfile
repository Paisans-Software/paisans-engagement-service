FROM golang:1.25.8-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /paisans-engagement-service ./cmd/paisans-engagement-service

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /paisans-engagement-service /paisans-engagement-service
EXPOSE 8083
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s CMD ["/paisans-engagement-service", "healthcheck"]
ENTRYPOINT ["/paisans-engagement-service"]
