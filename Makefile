GO       ?= go
BIN      ?= lt
BUILDDIR ?= build

# The VMs this gets shipped to: Linux on amd64.
DIST_OS   ?= linux
DIST_ARCH ?= amd64
DIST_BIN   = $(BUILDDIR)/$(BIN)-$(DIST_OS)-$(DIST_ARCH)

SOURCES := go.mod go.sum $(shell find cmd internal -name '*.go')

# -s -w drop the symbol table and DWARF data, which takes ~11MB down to ~7MB;
# nobody is going to run a debugger against the copy on the VMs.
GOFLAGS_DIST = -trimpath -ldflags '-s -w'

.PHONY: all
all: $(BIN)

$(BIN): $(SOURCES)
	$(GO) build -o $@ ./cmd/$(BIN)

.PHONY: dist
dist: $(DIST_BIN)

$(DIST_BIN): $(SOURCES) | $(BUILDDIR)
	GOOS=$(DIST_OS) GOARCH=$(DIST_ARCH) CGO_ENABLED=0 \
	  $(GO) build $(GOFLAGS_DIST) -o $@ ./cmd/$(BIN)

$(BUILDDIR):
	mkdir -p $@

.PHONY: publish
publish: dist
	@echo "publish: not yet implemented; $(DIST_BIN) is built and waiting" >&2
	@false

.PHONY: test
test:
	$(GO) test ./...

.PHONY: clean
clean:
	rm -rf $(BUILDDIR) $(BIN)
