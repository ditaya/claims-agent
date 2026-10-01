FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/claims-agent ./cmd/claims-agent

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && \
    adduser -D -u 10001 appuser
WORKDIR /app
COPY --from=build /out/claims-agent ./claims-agent
COPY data/policies.json ./data/policies.json
USER appuser
EXPOSE 8080
ENTRYPOINT ["./claims-agent"]
CMD ["serve", "-addr", ":8080"]
