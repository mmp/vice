FROM ubuntu
RUN mkdir -p /usr/local/src/vice


COPY --from=golang:1.24-bookworm /usr/local/go/ /usr/local/go/
ENV PATH="/usr/local/go/bin:${PATH}"

RUN apt-get update && apt-get install -y xorg-dev libsdl2-dev git build-essential
COPY . /usr/local/src/vice
#RUN cd /usr/local/src && git clone https://github.com/mmp/vice.git
RUN cd /usr/local/src/vice && go build -o vice

