module github.com/influenzanet/messaging-service

go 1.23.0

toolchain go1.23.6

require (
	github.com/coneno/logger v1.2.2
	github.com/golang/protobuf v1.5.3
	github.com/google/uuid v1.3.1
	github.com/influenzanet/go-utils v0.2.14
	github.com/influenzanet/logging-service v0.2.0
	github.com/influenzanet/study-service v1.7.2
	github.com/influenzanet/user-management-service v1.1.2
	github.com/jordan-wright/email v4.0.1-0.20210109023952-943e75fe5223+incompatible
	go.mongodb.org/mongo-driver v1.13.1
	go.uber.org/mock v0.6.0
	google.golang.org/grpc v1.60.1
	google.golang.org/protobuf v1.36.6
	gopkg.in/yaml.v2 v2.4.0
)

require (
	github.com/golang/snappy v0.0.4 // indirect
	github.com/klauspost/compress v1.17.4 // indirect
	github.com/montanaflynn/stats v0.7.1 // indirect
	github.com/xdg-go/pbkdf2 v1.0.0 // indirect
	github.com/xdg-go/scram v1.1.2 // indirect
	github.com/xdg-go/stringprep v1.0.4 // indirect
	github.com/youmark/pkcs8 v0.0.0-20201027041543-1326539a0a0a // indirect
	golang.org/x/crypto v0.18.0 // indirect
	golang.org/x/net v0.20.0 // indirect
	golang.org/x/sync v0.16.0 // indirect
	golang.org/x/sys v0.16.0 // indirect
	golang.org/x/text v0.14.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240108191215-35c7eff3a6b1 // indirect
)

replace github.com/influenzanet/user-management-service => ../user-management-service
