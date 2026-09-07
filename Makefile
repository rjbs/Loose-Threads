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
GOFLAGS_INSTALL = -trimpath

.PHONY: all
all: $(BIN)

$(BIN): $(SOURCES)
	$(GO) build -o $@ ./cmd/$(BIN)

.PHONY: install
install:
	$(GO) install $(GOFLAGS_INSTALL) ./cmd/$(BIN)
	@d="$$($(GO) env GOBIN)"; test -n "$$d" || d="$$($(GO) env GOPATH)/bin"; \
	  echo "installed $(BIN) to $$d/$(BIN)"

.PHONY: dist
dist: $(DIST_BIN)

$(DIST_BIN): $(SOURCES) | $(BUILDDIR)
	GOOS=$(DIST_OS) GOARCH=$(DIST_ARCH) CGO_ENABLED=0 \
	  $(GO) build $(GOFLAGS_DIST) -o $@ ./cmd/$(BIN)

$(BUILDDIR):
	mkdir -p $@

# The WebDAV collection to upload into, e.g. https://dav.example.com/bin/ --
# note the trailing slash.  Set it in the environment or on the command line;
# it is not checked in.
PUBLISH_URL ?= https://myfiles.fastmail.com/static.rjbs.cloud/bin.amd64/

CURL ?= curl

# Credentials come from ~/.netrc unless PUBLISH_USER and PUBLISH_PASS are set.
# Either way they reach curl through a config file on stdin rather than through
# argv, so the password never shows up in ps output. -- claude, 2026-09-06
.PHONY: publish
publish: dist
ifeq ($(strip $(PUBLISH_URL)),)
	@echo 'publish: set PUBLISH_URL to the WebDAV collection to upload into' >&2
	@false
else
	@printf '%s\n' \
	  'url = "$(PUBLISH_URL)$(notdir $(DIST_BIN))"' \
	  'upload-file = "$(DIST_BIN)"' \
	  $(if $(strip $(PUBLISH_USER)),'user = "$(PUBLISH_USER):$(PUBLISH_PASS)"',) \
	  'netrc-optional =' \
	  'fail-with-body =' \
	  'silent =' \
	  'show-error =' \
	  | $(CURL) -K -
	@echo 'published $(notdir $(DIST_BIN)) to $(PUBLISH_URL)'
endif

.PHONY: test
test:
	$(GO) test ./...

.PHONY: clean
clean:
	rm -rf $(BUILDDIR) $(BIN)
