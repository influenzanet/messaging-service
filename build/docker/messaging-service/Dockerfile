##########################
# STAGE 1
##########################
FROM golang:1.23-alpine AS builder
RUN apk update && apk add --no-cache git ca-certificates && update-ca-certificates
ENV GO111MODULE=on
# Prepare module directory
WORKDIR /go/src/github.com/influenzanet/messaging-service
# Copy only go.mod and go.sum first for better caching
COPY go.mod ./
COPY go.sum ./
RUN go mod download
# Now copy the rest of the sources
COPY . .
WORKDIR /go/src/github.com/influenzanet/messaging-service/cmd/messaging-service
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags '-extldflags "-static"' -o app .

##########################
# STAGE 2
##########################
FROM scratch
# we need those on scratch for connecting to mongoDB:
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /go/src/github.com/influenzanet/messaging-service/cmd/messaging-service/app /app/
WORKDIR /app
EXPOSE 5004:5004
CMD ["./app"]
