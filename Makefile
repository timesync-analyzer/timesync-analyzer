.PHONY: proto clean proto-clean update-protocol grafana_up grafana_down

proto:
	@echo "Generating Go code from protobuf..."
	@mkdir -p protocol/generated
	protoc --go_out=. \
       --go_opt=module=timesync-analyzer \
       protocol/proto/metrics.proto
	@echo "Done! Generated: protocol/generated/metrics.pb.go"

proto-clean:
	@echo "Cleaning generated protobuf files..."
	rm -rf protocol/generated/*.pb.go
	@echo "Done!"

update-protocol:
	@echo "Updating protocol submodule..."
	git submodule update --remote protocol
	@echo "Protocol updated!"

update-all: update-protocol proto
	@echo "All updated!"

run:
	go run cmd/analyzer/main.go

build:
	go build -o bin/analyzer cmd/analyzer/main.go

clean: proto-clean
	rm -rf bin/

db_up:
	docker compose --env-file ./config/.env up -d timescaledb

db_down:
	docker compose down -v timescaledb

grafana_up:
	docker compose --env-file ./config/.env up -d grafana

grafana_down:
	docker compose down grafana