# sfsp-docker-guard（標準ライブラリだけ。ビルド中にネットワークは使わない）。
# sfsp の Dockerfile と同じ golang:1.25-alpine を使う。テストが通らなければビルドを失敗させる
FROM golang:1.25-alpine AS build
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod *.go ./
COPY testdata ./testdata
RUN go vet ./... && go test ./... && go build -trimpath -ldflags="-s -w" -o /sfsp-docker-guard .

FROM scratch
COPY --from=build /sfsp-docker-guard /sfsp-docker-guard
USER 65534:65534
ENTRYPOINT ["/sfsp-docker-guard"]
