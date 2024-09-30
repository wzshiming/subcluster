FROM golang:alpine AS builder
WORKDIR /go/src/github.com/wzshiming/subcluster/
COPY . .
ENV CGO_ENABLED=0
RUN go install ./cmd/sublet

FROM alpine
COPY --from=builder /go/bin/sublet /usr/local/bin/
ENTRYPOINT [ "/usr/local/bin/sublet" ]
