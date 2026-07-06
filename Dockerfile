FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /lysa-export .

FROM gcr.io/distroless/static-debian12
COPY --from=build /lysa-export /lysa-export
EXPOSE 8080
ENV PORT=8080
# The export downloads through the browser by default. To also keep a copy on
# disk, run with -e OUT_DIR=/out and mount a volume there (e.g. -v $PWD:/out).
ENTRYPOINT ["/lysa-export"]
