FROM alpine:latest
ARG TARGETARCH
RUN apk add --no-cache ca-certificates tzdata
COPY config.yml /etc/distribution/config.yml
COPY registry-$TARGETARCH /bin/registry
VOLUME ["/var/lib/registry"]
EXPOSE 80
ENTRYPOINT ["registry"]
ENV TZ=Asia/Shanghai \
    IMAGE_COPY_MODE=sync \
    IMAGE_PULL_TIMEOUT=5m \
    IMAGE_REPULL_MIN_INTERVAL=5m \
    IMAGE_CLEAN_INTERVAL=12h \
	IMAGE_CLEAN_TAG_RETAIN_NUMS=2 \
	IMAGE_CLEAN_PROJECT=test \
    IMAGE_CLEAN_BEFORE_DAYS=1 \
    OTEL_TRACES_EXPORTER=none
CMD ["serve", "/etc/distribution/config.yml"]