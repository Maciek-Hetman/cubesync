# syntax=docker/dockerfile:1.7
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/cubing-sync ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/cubing-sync /usr/local/bin/cubing-sync
EXPOSE 43781
ENTRYPOINT ["/usr/local/bin/cubing-sync"]
CMD ["serve"]
