FROM rust:1.95-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends cmake libclang-dev && rm -rf /var/lib/apt/lists/*
RUN rustup component add rustfmt clippy --toolchain 1.95.0
ENV RUSTUP_TOOLCHAIN=1.95.0
WORKDIR /src
