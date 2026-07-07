FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/pg-crud .

# Distroless static: no shell, no package manager — minimal attack surface
# and image size for a CGO-free binary.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/pg-crud /pg-crud
# runMigrations resolves file://migrations relative to CWD (/).
COPY --from=build /src/migrations /migrations
EXPOSE 8080
ENTRYPOINT ["/pg-crud"]
