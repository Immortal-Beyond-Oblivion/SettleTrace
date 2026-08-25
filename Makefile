.PHONY: fmt lint test test-race compose-up compose-down terraform-validate check-no-em-dash

fmt:
	gofmt -w $$(find cmd internal tools -name '*.go' -type f 2>/dev/null)

lint: check-no-em-dash check-function-comments
	go vet ./...

test: check-no-em-dash
	go test ./...

test-race:
	go test -race ./...

compose-up:
	docker compose up -d mysql redis localstack

compose-down:
	docker compose down -v

terraform-validate:
	terraform -chdir=terraform fmt -check -recursive
	terraform -chdir=terraform validate

check-no-em-dash:
	@if rg -n --glob '!docs/**' --glob '!*.pdf' $$'\342\200\224' .; then echo 'em dash character is forbidden'; exit 1; fi

check-function-comments:
	python3 scripts/check_function_comments.py
