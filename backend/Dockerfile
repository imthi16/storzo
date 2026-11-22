FROM python:3.12-slim
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
WORKDIR /app
# Pick up OS security fixes published after the base image was built.
RUN apt-get update && apt-get upgrade -y --no-install-recommends && rm -rf /var/lib/apt/lists/*
RUN useradd -r -u 10001 appuser && mkdir -p /app/state && chown -R appuser:appuser /app
# rclone for Cloud Connect (docs/CLOUD_CONNECT.md): pinned exact version, SHA-256 verified
# against SHA256SUMS published at https://downloads.rclone.org/v1.76.0/SHA256SUMS
ARG RCLONE_VERSION=1.76.0
ARG RCLONE_SHA256=b00f00b3266c3764413afe599d3ec316f6f43b3e0ea8a0bc1d186d2133e46df7
RUN apt-get update && apt-get install -y --no-install-recommends curl unzip ca-certificates && \
    curl -fsSL -o /tmp/rclone.zip "https://downloads.rclone.org/v${RCLONE_VERSION}/rclone-v${RCLONE_VERSION}-linux-amd64.zip" && \
    echo "${RCLONE_SHA256}  /tmp/rclone.zip" | sha256sum -c - && \
    unzip -o /tmp/rclone.zip -d /tmp/rclone && \
    install -m 0755 "/tmp/rclone/rclone-v${RCLONE_VERSION}-linux-amd64/rclone" /usr/local/bin/rclone && \
    rm -rf /tmp/rclone.zip /tmp/rclone && apt-get purge -y curl unzip && apt-get autoremove -y && rm -rf /var/lib/apt/lists/*
COPY requirements.lock ./
RUN pip install --no-cache-dir --require-hashes -r requirements.lock
COPY app/ ./app/
USER 10001:10001
EXPOSE 8000
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000", "--proxy-headers"]
