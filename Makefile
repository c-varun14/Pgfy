.PHONY: install-deps frontend build test browser-test image integration
install-deps:
	pnpm --dir web install --frozen-lockfile --ignore-scripts
	go mod download
frontend:
	pnpm --dir web build
build: frontend
	go build -trimpath -o bin/pgfy ./cmd/pgfy
test: frontend
	go vet ./...
	go test -race ./...
	python3 -m unittest discover -s deploy/tests -v
	bash -n deploy/bootstrap.sh deploy/install.sh deploy/pgfyctl deploy/postgres/*.sh
browser-test: build
	pnpm --dir web exec playwright install chromium
	pnpm --dir web test:e2e
image:
	python3 scripts/build-image.py
integration: image
	python3 scripts/integration.py
