.PHONY: build test install clean

build:
	go build -o skillman .
	mkdir -p $(HOME)/.local/bin
	ln -sf $(CURDIR)/skillman $(HOME)/.local/bin/skillman

test:
	go test ./...

install:
	go install .

clean:
	rm -f skillman
