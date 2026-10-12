# syntax=docker/dockerfile:1
FROM node:26-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.27.2-alpine AS backend
ARG VERSION=0.1.0-alpha.2.security.4
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/ cmd/
COPY internal/ internal/
COPY web/ web/
COPY --from=frontend /src/frontend/dist/ web/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/varianotes ./cmd/varianotes

FROM alpine:3.24
ARG VERSION=0.1.0-alpha.2.security.4
ARG VCS_REF=""
LABEL org.opencontainers.image.title="VariaNotes" \
      org.opencontainers.image.description="Self-hosted Markdown notes with full control over your data" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/fred-head/varianotes" \
      org.opencontainers.image.url="https://github.com/fred-head/varianotes" \
      org.opencontainers.image.documentation="https://github.com/fred-head/varianotes/blob/main/README.md" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.revision="${VCS_REF}"
RUN apk upgrade --no-cache \
    && apk add --no-cache ca-certificates \
    && addgroup -S varianotes \
    && adduser -S -G varianotes -h /data varianotes \
    && mkdir -p /data/app /data/notebooks \
    && chown -R varianotes:varianotes /data
COPY --from=backend /out/varianotes /usr/local/bin/varianotes
COPY LICENSE /usr/share/licenses/varianotes/LICENSE
COPY THIRD-PARTY-NOTICES.md /usr/share/licenses/varianotes/THIRD-PARTY-NOTICES.md
USER varianotes
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["varianotes"]
