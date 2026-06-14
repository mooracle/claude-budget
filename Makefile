# claude-budget — build & distribution
#
#   make build        build the local binary
#   make test         go test ./...
#   make vet          go vet ./...
#   make check        vet + build + test gate (the plan's build/vet gate)
#   make update-rates re-derive cache tiers in data/claude-pricing.json
#   make build-all    cross-compile every release target into dist/
#   make clean        remove build output

BINARY    := claude-budget
DIST      := dist
# darwin/linux/windows x amd64/arm64 — kept in sync with .github/workflows/release.yml.
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

.PHONY: all build test vet check update-rates build-all clean

all: check build

build:
	go build -o $(BINARY) .

test:
	go test ./...

vet:
	go vet ./...

# Mirrors the plan's "go vet ./... && go build ./... && go test ./..." gate.
check: vet
	go build ./...
	go test ./...

update-rates:
	./scripts/update-rates.sh

# Cross-compile every release target. Each successful build doubles as a smoke
# test that the tree compiles for that GOOS/GOARCH (CGO disabled = pure-Go).
build-all: clean
	@mkdir -p $(DIST)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		out="$(DIST)/$(BINARY)-$$os-$$arch$$ext"; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags "-s -w" -o "$$out" . || exit 1; \
	done
	@echo "built $(words $(PLATFORMS)) targets into $(DIST)/"

clean:
	rm -rf $(DIST) $(BINARY)
