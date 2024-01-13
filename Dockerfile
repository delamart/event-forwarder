FROM golang:1.21 as build

WORKDIR /usr/src/app

# pre-copy/cache go.mod for pre-downloading dependencies and only redownloading them in subsequent builds if they change
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -o /usr/local/bin/event-forwarder ./cmd/main.go

FROM gcr.io/distroless/static:nonroot
COPY --from=build /usr/local/bin/event-forwarder /usr/local/bin/event-forwarder
CMD ["event-forwarder"]