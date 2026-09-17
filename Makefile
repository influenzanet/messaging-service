.PHONY: test api mock docker-email-client docker-message-scheduler docker-messaging-service messaging-service message-scheduler email-client-service

PROTO_BUILD_DIR = intermediate

# Where the api repository holding the proto contracts is checked out. The default
# matches the sibling layout the mock target already assumes; override it when the
# contracts live elsewhere, e.g. make api API_PATH=./api.
API_PATH ?= ../api

# Where binary are put
TARGET_DIR ?= ./

DOCKER_OPTS ?= --rm

# TEST_ARGS = -v | grep -c RUN
VERSION := $(shell git describe --tags --abbrev=0)

DOCKER_TAG ?= $(VERSION)

help:
	@echo "Service building targets"
	@echo "  test  : run test suites"
	@echo "  api: compile protobuf files for go"
	@echo "  api-email, api-messaging: compile one contract only"
	@echo "  mock: generate mockup Services for testing"
	@echo "  build: build services (env TARGET_DIR to define binary location)"
	@echo "  docker-email-client: build docker container for email_client_service"
	@echo "  docker-message-scheduler: build docker container for message scheduler"
	@echo "  docker-messaging-service: build docker container for messaging-service"

	@echo "Env:"
	@echo "  DOCKER_OPTS : default docker build options (default : $(DOCKER_OPTS))"
	@echo "  TEST_ARGS : Arguments to pass to go test call"

api:
	if [ ! -d $(PROTO_BUILD_DIR) ]; then mkdir -p $(PROTO_BUILD_DIR); else  find $(PROTO_BUILD_DIR) -type f -delete &&  mkdir -p $(PROTO_BUILD_DIR); fi
	find $(API_PATH)/email_client_service/*.proto -maxdepth 1 -type f -exec protoc {} --proto_path=$(API_PATH) --go_out=$(PROTO_BUILD_DIR) --go-grpc_out=$(PROTO_BUILD_DIR) \;
	find $(API_PATH)/messaging_service/*.proto -maxdepth 1 -type f -exec protoc {} --proto_path=$(API_PATH) --go_out=$(PROTO_BUILD_DIR) --go-grpc_out=$(PROTO_BUILD_DIR) \;
	find "./pkg/api" -delete
	mv $(PROTO_BUILD_DIR)/github.com/influenzanet/messaging-service/pkg/api pkg/api
	find $(PROTO_BUILD_DIR) -delete

.PHONY: api-email api-messaging

# One contract at a time. The two packages under pkg/api were generated with different
# toolchains, so the api target above, which runs over both, rewrites the headers of the
# one nobody changed. These regenerate a single package and leave the other one alone.
api-email: PROTO_PACKAGE = email_client_service
api-messaging: PROTO_PACKAGE = messaging_service

api-email api-messaging:
	if [ ! -d $(PROTO_BUILD_DIR) ]; then mkdir -p $(PROTO_BUILD_DIR); else  find $(PROTO_BUILD_DIR) -type f -delete &&  mkdir -p $(PROTO_BUILD_DIR); fi
	find $(API_PATH)/$(PROTO_PACKAGE)/*.proto -maxdepth 1 -type f -exec protoc {} --proto_path=$(API_PATH) --go_out=$(PROTO_BUILD_DIR) --go-grpc_out=$(PROTO_BUILD_DIR) \;
	find "./pkg/api/$(PROTO_PACKAGE)" -delete
	mv $(PROTO_BUILD_DIR)/github.com/influenzanet/messaging-service/pkg/api/$(PROTO_PACKAGE) pkg/api/$(PROTO_PACKAGE)
	find $(PROTO_BUILD_DIR) -delete

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
