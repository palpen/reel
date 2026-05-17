.PHONY: build install uninstall test clean

VERSION := $(shell git describe --dirty --always 2>/dev/null || echo dev)
BIN     := reel
BINDIR  := $(HOME)/.local/bin

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) .

install: build
	@mkdir -p $(BINDIR)
	@install -m 0755 $(BIN) $(BINDIR)/$(BIN)
	@echo "Installed $(BIN) $(VERSION) to $(BINDIR)/"
	@case ":$$PATH:" in *":$(BINDIR):"*) ;; \
	  *) echo "WARNING: $(BINDIR) is not on PATH. Add to ~/.zshrc:"; \
	     echo "    export PATH=\"$(BINDIR):\$$PATH\"" ;; \
	esac

uninstall:
	rm -f $(BINDIR)/$(BIN)

test:
	go test -race -count=1 ./...

clean:
	rm -f $(BIN)
