# syntax=docker/dockerfile:1
FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/hookrelay ./cmd/hookrelay
RUN CGO_ENABLED=0 go build -o /out/chaosrecv ./cmd/chaosrecv
RUN CGO_ENABLED=0 go build -o /out/faultbench ./cmd/faultbench

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/hookrelay /hookrelay
COPY --from=build /out/chaosrecv /chaosrecv
EXPOSE 8080
ENTRYPOINT ["/hookrelay"]
