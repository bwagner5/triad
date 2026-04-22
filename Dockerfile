ARG GO_VERSION=1.26
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /triad ./cmd/triad

FROM scratch
COPY --from=build /triad /triad
ENTRYPOINT ["/triad"]
