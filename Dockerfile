ARG GO_VERSION=1.26
FROM golang:${GO_VERSION}-alpine AS build
RUN apk add --no-cache make git
WORKDIR /src
ARG GOPROXY
ENV GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN make build VERSION=$VERSION

FROM scratch
COPY --from=build /src/build/triad /triad
ENTRYPOINT ["/triad"]
