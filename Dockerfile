FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./
COPY internal/ internal/

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
  CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
  go build -trimpath -ldflags="-s -w" -o /out/cast-hbo-ad-skipper .


FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/cast-hbo-ad-skipper /cast-hbo-ad-skipper

ENTRYPOINT ["/cast-hbo-ad-skipper"]
