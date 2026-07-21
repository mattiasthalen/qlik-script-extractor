# syntax=docker/dockerfile:1

# ---- build stage -----------------------------------------------------------
FROM golang:1.25 AS build

WORKDIR /src

# Cache modules first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static, stripped binary — no cgo so it runs on distroless/static (or scratch).
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /qlik-parser .

# ---- runtime stage ---------------------------------------------------------
# distroless/static ships ca-certificates and a nonroot user, nothing else.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /qlik-parser /qlik-parser

USER nonroot:nonroot
ENV PORT=8080
ENV TMPDIR=/tmp
EXPOSE 8080

ENTRYPOINT ["/qlik-parser"]
CMD ["serve"]
