FROM golang:1.23-alpine AS build
WORKDIR /src
COPY . .
RUN go vet ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /token-server .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /token-server /token-server
ENV PORT=7000 HOST=0.0.0.0
EXPOSE 7000
# distroless has no shell or curl; the binary answers /health for an external checker.
ENTRYPOINT ["/token-server"]
