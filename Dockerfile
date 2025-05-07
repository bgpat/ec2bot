FROM golang:1.24.3-alpine3.21

RUN apk add -U ca-certificates curl git gcc musl-dev make
ENV GO111MODULE=on

RUN mkdir -p $GOPATH/src/github.com/bgpat/ec2bot
WORKDIR $GOPATH/src/github.com/bgpat/ec2bot

COPY go.mod go.sum ./
RUN go mod download

ADD . ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w -extldflags '-static'" -o /ec2bot


#FROM alpine:3.10
FROM scratch
COPY --from=0 /ec2bot /ec2bot
COPY --from=0 /etc/ssl /etc/ssl
EXPOSE 3000
ENTRYPOINT ["/ec2bot"]
