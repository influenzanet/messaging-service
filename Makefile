.PHONY: test api api-check mock docker-email-client docker-message-scheduler docker-messaging-service messaging-service message-scheduler email-client-service

PROTO_BUILD_DIR = intermediate

# Where the api repository holding the proto contracts is checked out: by default the api
# submodule, whose gitlink pins the contract version (git submodule update --init). Override
# it to build against another checkout, e.g. make api API_PATH=../api.
API_PATH ?= ./api

include proto-toolchain.mk

# Where binary are put
TARGET_DIR ?= ./

DOCKER_OPTS ?= --rm

# TEST_ARGS = -v | grep -c RUN
VERSION := $(shell git describe --tags --abbrev=0)

DOCKER_TAG ?= $(VERSION)

help:
	@echo "Service building targets"
	@echo "  test  : run test suites"
	@echo "  api: compile protobuf files for go with the pinned toolchain (proto-toolchain.mk)"
	@echo "  api-check: regenerate pkg/api and fail if it differs from the committed code"
	@echo "  mock: generate mockup Services for testing"
	@echo "  build: build services (env TARGET_DIR to define binary location)"
	@echo "  docker-email-client: build docker container for email_client_service"
	@echo "  docker-message-scheduler: build docker container for message scheduler"
	@echo "  docker-messaging-service: build docker container for messaging-service"

	@echo "Env:"
	@echo "  DOCKER_OPTS : default docker build options (default : $(DOCKER_OPTS))"
	@echo "  TEST_ARGS : Arguments to pass to go test call"

# pkg/api is deleted and replaced only once every proto has been generated: a protoc that
# fails, or cannot run on this machine, stops the recipe first and leaves pkg/api alone.
api: $(PROTO_TOOLS)
	if [ ! -d $(PROTO_BUILD_DIR) ]; then mkdir -p $(PROTO_BUILD_DIR); else  find $(PROTO_BUILD_DIR) -type f -delete &&  mkdir -p $(PROTO_BUILD_DIR); fi
	for f in $(API_PATH)/email_client_service/*.proto; do $(PROTOC_CMD) $$f --proto_path=$(API_PATH) --go_out=$(PROTO_BUILD_DIR) --go-grpc_out=$(PROTO_BUILD_DIR) || exit 1; done
	for f in $(API_PATH)/messaging_service/*.proto; do $(PROTOC_CMD) $$f --proto_path=$(API_PATH) --go_out=$(PROTO_BUILD_DIR) --go-grpc_out=$(PROTO_BUILD_DIR) || exit 1; done
	find "./pkg/api" -delete
	mv $(PROTO_BUILD_DIR)/github.com/influenzanet/messaging-service/pkg/api pkg/api
	find $(PROTO_BUILD_DIR) -delete

# Drift check: fails when pkg/api is not what the pinned toolchain generates from the pinned
# contracts (a hand edit, a proto change not regenerated, another toolchain, a stale file).
api-check: api
	git diff --exit-code -- pkg/api
	@untracked="$$(git ls-files --others --exclude-standard -- pkg/api)"; \
	test -z "$$untracked" || { echo "generated but not committed:"; echo "$$untracked"; exit 1; }

mock:
	mockgen -source=./pkg/api/email_client_service/email-client-service_grpc.pb.go EmailClientServiceApiClient > test/mocks/email-client-service/email_client_service.go
	mockgen github.com/influenzanet/user-management-service/pkg/api UserManagementApiClient,UserManagementApi_StreamUsersClient > test/mocks/user-management-service/user_management_service.go
	mockgen github.com/influenzanet/study-service/pkg/api StudyServiceApiClient > test/mocks/study-service/study_service.go
	mockgen github.com/influenzanet/logging-service/pkg/api LoggingServiceApiClient > test/mocks/logging_service/logging_service.go

test:
	./test/test.sh $(TEST_ARGS)

docker-email-client:
	docker build -t  github.com/influenzanet/email-client-service:$(DOCKER_TAG)  -f build/docker/email-client-service/Dockerfile $(DOCKER_OPTS) .

docker-message-scheduler:
	docker build -t  github.com/influenzanet/message-scheduler:$(DOCKER_TAG)  -f build/docker/message-scheduler/Dockerfile $(DOCKER_OPTS) .

docker-messaging-service:
	docker build -t github.com/influenzanet/messaging-service:$(DOCKER_TAG)  -f build/docker/messaging-service/Dockerfile $(DOCKER_OPTS) .

messaging-service:
	go build -o $(TARGET_DIR) ./cmd/messaging-service

message-scheduler:
	go build -o $(TARGET_DIR) ./cmd/message-scheduler

email-client-service:
	go build -o $(TARGET_DIR) ./cmd/email-client-service

build: messaging-service message-scheduler email-client-service

docker: docker-message-scheduler docker-messaging-service docker-email-client
