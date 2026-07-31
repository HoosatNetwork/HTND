ARG HOOSATD_IMAGE
ARG HOOSATMINER_IMAGE

FROM ${HOOSATD_IMAGE} as htnd
FROM ${HOOSATMINER_IMAGE} as hoosatminer

FROM golang:1.19-alpine

RUN mkdir -p /go/src/github.com/HoosatNetwork/HTND

WORKDIR /go/src/github.com/HoosatNetwork/HTND

RUN apk add bash build-base git

COPY go.mod .
COPY go.sum .

RUN go mod download

COPY . .

COPY --from=htnd /app/ /app/
COPY --from=hoosatminer /app/ /app/
ENV PATH="/app:${PATH}"

WORKDIR /go/src/github.com/HoosatNetwork/HTND/stability-tests

RUN go install ./...

ENTRYPOINT ["./run/run.sh"]