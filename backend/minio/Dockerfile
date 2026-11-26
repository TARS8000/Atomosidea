# MinIO（サーバー）と mc（クライアント）のイメージ。minio サービスと minio-init が同じイメージを使う。
# MinIO は Docker Hub（minio/minio・minio/mc）と quay.io でのイメージ配布をやめ、ソースだけの配布になったので、
# 今まで使っていた版（minio/minio:latest・minio/mc:latest の最後の版）をソースからビルドする。
# 版を上げるときは下の ARG を変え、Version は ReleaseTag の時刻部分を「YYYY-MM-DDTHH:MM:SSZ」にしたものにする
FROM golang:1.25-alpine AS build
ARG MINIO_RELEASE=RELEASE.2025-09-07T16-13-09Z
ARG MINIO_VERSION=2025-09-07T16:13:09Z
ARG MC_RELEASE=RELEASE.2025-08-13T08-35-41Z
ARG MC_VERSION=2025-08-13T08:35:41Z
ENV CGO_ENABLED=0
RUN go install -trimpath -ldflags "-s -w -X github.com/minio/minio/cmd.Version=${MINIO_VERSION} -X github.com/minio/minio/cmd.ReleaseTag=${MINIO_RELEASE}" github.com/minio/minio@${MINIO_RELEASE} \
 && go install -trimpath -ldflags "-s -w -X github.com/minio/mc/cmd.Version=${MC_VERSION} -X github.com/minio/mc/cmd.ReleaseTag=${MC_RELEASE}" github.com/minio/mc@${MC_RELEASE}

FROM alpine:3.22
# curl は minio の healthcheck、sh は minio-init のスクリプトが使う（公式イメージと同じく root で動かす）
RUN apk add --no-cache ca-certificates curl
COPY --from=build /go/bin/minio /go/bin/mc /usr/bin/
EXPOSE 9000 9001
ENTRYPOINT ["minio"]
