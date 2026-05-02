.PHONY: harness-check docs-check test vet fmt-check e2e-compare-docker e2e-lifecycle

harness-check:
	./scripts/harness-check.sh

docs-check:
	./scripts/harness-check.sh --docs-only

test:
	go test ./...

vet:
	go vet ./...

fmt-check:
	@if [ -n "$$(gofmt -l $$(find cmd internal pkg test -name '*.go' -print | sort))" ]; then \
		gofmt -l $$(find cmd internal pkg test -name '*.go' -print | sort); \
		exit 1; \
	fi

e2e-compare-docker:
	./scripts/harness-check.sh --docker-e2e

e2e-lifecycle:
	./scripts/harness-check.sh --lifecycle
