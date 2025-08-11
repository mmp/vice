CONTAINER_ID = $(shell docker ps -alq)

all:
	go build -o vice ./cmd/vice

windows:
	go build -ldflags -H=windowsgui -o ./vice.exe ./cmd/vice

docker: 
	docker build . -t vice
	docker run -d vice
	docker cp $(CONTAINER_ID):/usr/local/src/vice/vice .

