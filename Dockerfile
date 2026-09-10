FROM caddy:2-alpine@sha256:5f5c8640aae01df9654968d946d8f1a56c497f1dd5c5cda4cf95ab7c14d58648
ARG REVISION=local
LABEL org.opencontainers.image.title="Dylan's Lawn Care website demo"
LABEL org.opencontainers.image.revision=$REVISION
COPY Caddyfile /etc/caddy/Caddyfile
COPY dist/ /srv/
RUN printf '{"revision":"%s"}\n' "$REVISION" > /opt/site-version.json
EXPOSE 8080
HEALTHCHECK --interval=5s --timeout=3s --start-period=5s --retries=12 CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
