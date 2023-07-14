FROM golang:1.20.4-alpine AS builder

RUN apk add --no-cache ca-certificates git zeromq-dev gcc alpine-sdk

RUN mkdir /user && \
    echo 'nobody:x:65534:65534:nobody:/:' > /user/passwd && \
    echo 'nobody:x:65534:' > /user/group

WORKDIR /src
COPY . .
RUN go mod tidy
RUN CGO_ENABLED=1 go build -installsuffix 'static' -o /node
RUN ldd /node | tr -s '[:blank:]' '\n' | grep '^/' | \
    xargs -I % sh -c 'mkdir -p $(dirname deps%); cp % deps%;'

FROM alpine AS final

RUN apk add --update bind-tools
COPY --from=builder /user/group /user/passwd /etc/
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /node /node
COPY --from=builder /src/deps /
USER nobody:nobody

EXPOSE 10000

CMD [""]
