FROM alpine:3.19

RUN apk add --no-cache transmission-daemon

RUN mkdir -p /config /downloads

COPY settings.json /config/settings.json

EXPOSE 9091 51413 51413/udp

CMD ["transmission-daemon", "-f", "-g", "/config"]
