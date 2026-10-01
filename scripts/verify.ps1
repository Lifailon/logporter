go vet ./...
go build ./... > $null

go install github.com/mfridman/tparse@latest
go test ./... -json -cover | tparse -all -format=basic

go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
golangci-lint run ./...

node --test internal/dashboard/dashboard.test.mjs