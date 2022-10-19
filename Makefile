test:
	go test ./... -timeout=60s -count=1
	go build .