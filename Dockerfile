FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /lysa-export .

FROM gcr.io/distroless/static-debian12
COPY --from=build /lysa-export /lysa-export
VOLUME ["/out"]
EXPOSE 8080
ENV OUT_DIR=/out PORT=8080
ENTRYPOINT ["/lysa-export"]
