FROM rust:1-bookworm AS builder

WORKDIR /app

COPY Cargo.toml Cargo.lock ./
COPY src ./src
COPY tools ./tools
COPY slides ./slides
COPY feedback-qr.txt ./

RUN cargo build --release

FROM debian:bookworm-slim AS runtime

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        awscli \
        bash \
        ca-certificates \
        curl \
        git \
        less \
    && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /workspace /home/yolomancer/.aws /home/yolomancer/.yolomancer \
    && chmod 0777 /workspace /home/yolomancer /home/yolomancer/.aws /home/yolomancer/.yolomancer

COPY --from=builder /app/target/release/yolomancer /usr/local/bin/yolomancer
COPY --from=builder /app/tools /opt/yolomancer/tools
COPY --from=builder /app/slides /opt/yolomancer/slides
COPY --from=builder /app/feedback-qr.txt /opt/yolomancer/feedback-qr.txt

WORKDIR /workspace

ENV HOME=/home/yolomancer
ENV YOLOMANCER_WRITABLE_ROOTS=/workspace

ENTRYPOINT ["yolomancer"]
