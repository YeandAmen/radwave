# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /radar ./cmd/radar && mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /radar /radar
COPY --from=build --chown=65532:65532 /data /data
ENV RADAR_ADDR=0.0.0.0:8080 RADAR_DB=/data/radar.db
USER 65532:65532
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/radar"]
CMD ["serve"]
