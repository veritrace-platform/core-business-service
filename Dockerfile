FROM golang:1.27.1-alpine3.24 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/veritrace-platform/core-business-service/internal/platform/buildinfo.Version=${VERSION}" \
      -o /out/core-business-service ./cmd/core-business-service

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/core-business-service /app/core-business-service
USER nonroot:nonroot
EXPOSE 8080 8081
ENTRYPOINT ["/app/core-business-service"]
CMD ["serve"]
