FROM golang:1.10-alpine3.7

RUN apk add -U ca-certificates curl git gcc musl-dev make
RUN curl -fsSL -o /usr/local/bin/dep https://github.com/golang/dep/releases/download/v0.4.1/dep-linux-amd64 \
		&& chmod +x /usr/local/bin/dep

RUN mkdir -p $GOPATH/src/github.com/bgpat/ec2bot
WORKDIR $GOPATH/src/github.com/bgpat/ec2bot

COPY Gopkg.toml Gopkg.lock ./
RUN dep ensure -vendor-only -v

ADD . ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w -extldflags '-static'" -o /ec2bot


#FROM alpine:3.7
FROM scratch
COPY --from=0 /ec2bot /ec2bot
COPY --from=0 /etc/ssl /etc/ssl
EXPOSE 3000
ENTRYPOINT ["/ec2bot"]
