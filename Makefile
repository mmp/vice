CONTAINER_ID = $(shell docker ps -alq)

all:
	go build -o vice

windows:
	go build -ldflags -H=windowsgui -o ./vice.exe .

docker: 
	docker build . -t vice
	docker run -d vice
	docker cp $(CONTAINER_ID):/usr/local/src/vice/vice .

