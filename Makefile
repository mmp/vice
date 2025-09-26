CONTAINER_ID = $(shell docker ps -alq)

all: lfs-pull
	go build -o vice ./cmd/vice

windows: lfs-pull
	go build -ldflags -H=windowsgui -o ./vice.exe ./cmd/vice

lfs-pull:
	git lfs pull

docker: 
	docker build . -t vice
	docker run -d vice
	docker cp $(CONTAINER_ID):/usr/local/src/vice/vice .

