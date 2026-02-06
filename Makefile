CONTAINER_ID = $(shell docker ps -alq)

all: lfs-pull
	./build.sh

windows: lfs-pull
	./build.bat

lfs-pull:
	git lfs pull

docker: 
	docker build . -t vice
	docker run -d vice
	docker cp $(CONTAINER_ID):/usr/local/src/vice/vice .

