.PHONY: proto clean proto-clean update-protocol grafana_up grafana_down \
        docker_build analyzer_up analyzer_down analyzer_logs \
        reportd_up reportd_down reportd_logs

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

db_up:
	docker compose --env-file ./config/.env up -d timescaledb

db_down:
	docker compose down -v timescaledb

grafana_up:
	docker compose --env-file ./config/.env up -d grafana

grafana_down:
	docker compose down grafana

docker_build:
	docker compose --env-file ./config/.env build analyzer

analyzer_up:
	docker compose --env-file ./config/.env up -d analyzer

analyzer_down:
	docker compose down analyzer

analyzer_logs:
	docker compose logs -f analyzer

reportd_up:
	docker compose --env-file ./config/.env up -d reportd

reportd_down:
	docker compose down reportd

reportd_logs:
	docker compose logs -f reportd
