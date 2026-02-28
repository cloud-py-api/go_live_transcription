# SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
# SPDX-FileCopyrightText: 2020 Alpha Cephei Inc. and contributors
# SPDX-License-Identifier: AGPL-3.0-or-later

ARG RT_IMAGE=ubuntu:22.04

# =============================================================================
# Stage 1: Build Kaldi + Vosk from source
# =============================================================================
FROM ${RT_IMAGE} AS vosk-builder

ARG HAVE_CUDA
ARG KALDI_MKL
ARG DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
        curl \
        wget \
        bzip2 \
        unzip \
        xz-utils \
        g++ \
        make \
        cmake \
        git \
        zlib1g-dev \
        automake \
        autoconf \
        libtool \
        pkg-config \
        ca-certificates \
        gfortran \
        python3 \
    && rm -rf /var/lib/apt/lists/*

# Build Kaldi
RUN COMMIT=bc5baf14231660bd50b7d05788865b4ac6c34481 \
    && for attempt in 1 2 3; do \
         git clone -c remote.origin.fetch=+${COMMIT}:refs/remotes/origin/$COMMIT \
             --no-checkout --progress --depth 1 \
             https://github.com/alphacep/kaldi /opt/kaldi \
         && break \
         || { echo "Clone attempt $attempt failed, retrying in 10s..."; rm -rf /opt/kaldi; sleep 10; }; \
       done \
    && cd /opt/kaldi \
    && git checkout $COMMIT \
    && curl -o /opt/kaldi/tools/extras/install_mkl.sh \
        https://raw.githubusercontent.com/kaldi-asr/kaldi/aef1d98603b68e6cf3a973e9dcd71915e2a175fe/tools/extras/install_mkl.sh \
    && cd /opt/kaldi/tools \
    && sed -i 's:status=0:exit 0:g' extras/check_dependencies.sh \
    && sed -i 's:--enable-ngram-fsts:--enable-ngram-fsts --disable-bin:g' Makefile \
    && sed -i 's: -msse -msse2 : -msse -msse2 -mavx -mavx2 :' /opt/kaldi/src/makefiles/linux_x86_64_mkl.mk \
    && sed -i 's: -msse -msse2 : -msse -msse2 -mavx -mavx2 :' /opt/kaldi/src/makefiles/linux_openblas.mk \
    && sed -i 's: -msse -msse2: -msse -msse2 -mavx -mavx2:' /opt/kaldi/tools/Makefile \
    && make -j $(nproc) openfst cub \
    && if [ "x$KALDI_MKL" != "x1" ] ; then \
          extras/install_openblas_clapack.sh; \
       else \
          extras/install_mkl.sh; \
       fi \
    \
    && cd /opt/kaldi/src \
    && HAVE_CUDA_OPN=$(if [ "x$HAVE_CUDA" != "x1" ]; then echo "--use-cuda=no"; else echo "--use-cuda"; fi) \
    && MATHLIB=$(if [ "x$KALDI_MKL" != "x1" ]; then echo "OPENBLAS_CLAPACK"; else echo "MKL"; fi) \
    # Skip Maxwell (sm_50/sm_52): smaller binaries, faster compilation
    && if [ "x$HAVE_CUDA" = "x1" ]; then \
         export CUDA_ARCH="-gencode arch=compute_60,code=sm_60 -gencode arch=compute_61,code=sm_61 -gencode arch=compute_70,code=sm_70 -gencode arch=compute_75,code=sm_75 -gencode arch=compute_80,code=sm_80 -gencode arch=compute_86,code=sm_86 -gencode arch=compute_89,code=sm_89 -gencode arch=compute_90,code=sm_90"; \
       fi \
    && ./configure --mathlib=$MATHLIB --shared $HAVE_CUDA_OPN \
    && sed -i 's:-msse -msse2:-msse -msse2 -mavx -mavx2:g' kaldi.mk \
    && sed -i 's: -O1 : -O3 :g' kaldi.mk \
    && if [ "x$HAVE_CUDA" != "x1" ]; then \
          make -j $(nproc) online2 lm rnnlm; \
       else \
          make -j $(nproc) online2 lm rnnlm cudafeat cudadecoder; \
       fi

# Build Vosk API
RUN COMMIT=0f364e3a4407fbc837f37423223dff9c7b3e8557 \
    && for attempt in 1 2 3; do \
         git clone -c remote.origin.fetch=+${COMMIT}:refs/remotes/origin/$COMMIT \
             --no-checkout --progress --depth 1 \
             https://github.com/alphacep/vosk-api /opt/vosk-api \
         && break \
         || { echo "Clone attempt $attempt failed, retrying in 10s..."; rm -rf /opt/vosk-api; sleep 10; }; \
       done \
    && cd /opt/vosk-api \
    && git checkout $COMMIT \
    && cd /opt/vosk-api/src \
    && sed -i 's/ -lopenblas -llapack -lblas -lf2c/ -lopenblas -llapack -lblas -lf2c -lcblas/' Makefile \
    && HAVE_OPENBLAS=$(if [ "x$KALDI_MKL" = "x1" ]; then echo "0"; else echo "1"; fi) \
    && HAVE_CUDA=$HAVE_CUDA HAVE_MKL=$KALDI_MKL HAVE_OPENBLAS_CLAPACK=$HAVE_OPENBLAS \
       KALDI_ROOT=/opt/kaldi make -j $(nproc) \
    && [ "x$HAVE_CUDA" != "x1" ] || ln -sf /usr/local/cuda/compat/libcuda.so.1 /lib/x86_64-linux-gnu/

# Collect runtime libraries needed by libvosk.so into /opt/vosk-runtime
RUN mkdir -p /opt/vosk-runtime \
    && cp /opt/vosk-api/src/libvosk.so /opt/vosk-runtime/ \
    && cp /opt/vosk-api/src/vosk_api.h /opt/vosk-runtime/ \
    # Copy non-standard shared libs that libvosk.so depends on
    && ldd /opt/vosk-api/src/libvosk.so | awk '/=>/{print $3}' | sort -u | while read lib; do \
         case "$lib" in \
           /opt/*|/usr/local/cuda/*) cp -L "$lib" /opt/vosk-runtime/ 2>/dev/null || true ;; \
         esac; \
       done \
    # Copy all MKL runtime libs (libmkl_rt.so dynamically loads these at runtime)
    && if [ -d /opt/intel/mkl/lib/intel64 ]; then \
         cp -L /opt/intel/mkl/lib/intel64/libmkl_*.so /opt/vosk-runtime/ 2>/dev/null || true; \
       fi \
    # Copy OpenBLAS runtime libs (for CUDA variant)
    && if [ -d /opt/kaldi/tools/OpenBLAS/install/lib ]; then \
         cp -L /opt/kaldi/tools/OpenBLAS/install/lib/lib*.so* /opt/vosk-runtime/ 2>/dev/null || true; \
       fi \
    && ls -la /opt/vosk-runtime/

# =============================================================================
# Stage 2: Build Go binary
# =============================================================================
FROM golang:1.24-bookworm AS go-builder

ARG DEBIAN_FRONTEND=noninteractive

# Install CGO dependencies (opus codec)
RUN apt-get update && apt-get install -y --no-install-recommends \
        libopus-dev \
        libopusfile-dev \
    && rm -rf /var/lib/apt/lists/*

# Copy Vosk library + header + runtime dependencies from builder
COPY --from=vosk-builder /opt/vosk-runtime/ /opt/vosk/

WORKDIR /build

# Cache Go module downloads
COPY go.mod go.sum ./
RUN go mod download

# Build the binary
COPY . .
ENV CGO_ENABLED=1
ENV CGO_CFLAGS="-I/opt/vosk"
ENV CGO_LDFLAGS="-L/opt/vosk -lvosk -Wl,-rpath-link,/opt/vosk -Wl,--unresolved-symbols=ignore-in-shared-libs"
RUN go build -o /live_transcription .

# =============================================================================
# Stage 3: Runtime
# =============================================================================
FROM ${RT_IMAGE}

ARG HAVE_CUDA
ARG DEBIAN_FRONTEND=noninteractive

# Install runtime dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
        libopus0 \
        libopusfile0 \
        ca-certificates \
        curl \
        procps \
    && rm -rf /var/lib/apt/lists/*

# Download and install FRP client with checksum verification
# FRP version and checksums - update these when upgrading
ARG FRP_VERSION=0.61.1
ARG FRP_AMD64_SHA256=bff260b68ca7b1461182a46c4f34e9709ba32764eed30a15dd94ac97f50a2c40
ARG FRP_ARM64_SHA256=af6366f2b43920ebfe6235dba6060770399ed1fb18601e5818552bd46a7621f8

RUN set -ex; \
    ARCH=$(uname -m); \
    if [ "$ARCH" = "aarch64" ]; then \
        FRP_ARCH="arm64"; \
        FRP_SHA256="${FRP_ARM64_SHA256}"; \
    else \
        FRP_ARCH="amd64"; \
        FRP_SHA256="${FRP_AMD64_SHA256}"; \
    fi; \
    FRP_URL="https://github.com/fatedier/frp/releases/download/v${FRP_VERSION}/frp_${FRP_VERSION}_linux_${FRP_ARCH}.tar.gz"; \
    echo "Downloading FRP v${FRP_VERSION} for ${FRP_ARCH}..."; \
    curl -fsSL "${FRP_URL}" -o /tmp/frp.tar.gz; \
    ACTUAL_SHA256=$(sha256sum /tmp/frp.tar.gz | cut -d' ' -f1); \
    if [ "$ACTUAL_SHA256" != "$FRP_SHA256" ]; then \
        echo "Checksum verification failed for FRP v${FRP_VERSION} (${FRP_ARCH})"; \
        echo "Expected: ${FRP_SHA256}"; \
        echo "Got:      ${ACTUAL_SHA256}"; \
        exit 1; \
    fi; \
    tar -C /tmp -xzf /tmp/frp.tar.gz; \
    cp /tmp/frp_${FRP_VERSION}_linux_${FRP_ARCH}/frpc /usr/local/bin/frpc; \
    chmod +x /usr/local/bin/frpc; \
    rm -rf /tmp/frp_${FRP_VERSION}_linux_${FRP_ARCH} /tmp/frp.tar.gz; \
    echo "FRP client installed successfully"

# Copy Vosk runtime libraries (libvosk.so + math libs)
COPY --from=vosk-builder /opt/vosk-runtime/ /opt/vosk/
ENV LD_LIBRARY_PATH=/opt/vosk
ENV MKL_THREADING_LAYER=SEQUENTIAL

# CUDA compatibility symlink
RUN if [ "x$HAVE_CUDA" = "x1" ] && [ -d /usr/local/cuda/compat ]; then \
      ln -sf /usr/local/cuda/compat/libcuda.so.1 /lib/x86_64-linux-gnu/; \
    fi

# Copy application binary
COPY --from=go-builder /live_transcription /live_transcription

# Copy scripts
COPY --chmod=775 docker/start.sh /start.sh
COPY --chmod=775 docker/healthcheck.sh /healthcheck.sh

# Copy app metadata
COPY appinfo /appinfo

ENTRYPOINT ["/start.sh"]
HEALTHCHECK --interval=20s --timeout=2s --retries=300 CMD /healthcheck.sh
