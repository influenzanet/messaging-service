# Messaging services for the Influenzanet system

This is a Go implementation of the [Messaging Services](https://github.com/influenzanet/influenzanet/wiki/Services#user-management-service)

It consist three services:
- messagine service for handling higher level messaging logic
- message schedular is a job for sending out automatic emails and manage outgoing
- email-client-service: a wrapper for SMTP client

## Email client config files
The email-client-service expects two configuration files at the MESSAGING_CONFIG_FOLDER path:
- `high-prio-smtp-servers.yaml` -> server list that will be used to send instant messages (e.g. login verification code)
- `smtp-servers.yaml` -> server list that will be used to send bulk messages (e.g., weekly study emails)

The two files follow the same structure and allow the same configuration options. (See example in /test/configs)

## WhatsApp scheduler retries

Failures that a single message cannot cause stop the current instance's tick at once,
without consuming any message's five-attempt retry budget: HTTP 401/403, Meta
authentication, account and sender-number codes (`33`, `131045`, and the Graph shape
`100` with subcode `33`), HTTP 429 and Meta rate-limit codes other than `131056`, and a
template Meta has paused or disabled (`132015`, `132016`). An HTTP 408 or 5xx status, or a
transport error, is read as transient before any Meta code is considered. Every claimed
message keeps its `lastSendAttempt` lock until its normal expiry. A paused or disabled
template therefore also holds back the other campaigns of that instance until an operator
acts on it: the stop is logged with the failure class and the message type.

Transient failures (HTTP 408 and 5xx, Meta `is_transient` other than on `131056`, codes
`1`, `2`, `131000`, `131016`, `131057`, transport errors) can belong to one message or to
an outage, so one such failure is held: if the next message that reaches the sender fails
transiently too, the tick stops with nothing charged; if it succeeds or fails for a
message-specific reason, the held message was at fault and is charged one attempt like
any other failure. A failure left undecided at the end of the tick, or whose claim may
have expired meanwhile, is not charged. This keeps an outage from draining the queue and
keeps one persistently failing message from blocking the messages behind it, at a price
that is stated here: during a partial outage, a message that fails transiently while its
neighbour succeeds is charged, and after five such charges it is archived undelivered;
two messages that both keep failing transiently and sit next to each other in the fetch
order are read as an outage on every tick, so nothing in the scheduler ever decides them,
and they block that instance's queue until one of them is removed or delivers. The
queue-age guard that would bound both remains a separate product decision. The circuit
is per tick/instance, not a persistent or cross-instance breaker. Meta pair-rate limit
`131056` preserves the retry budget but only defers that recipient, and does not decide a
held failure.

Message-specific failures (invalid template parameters, unknown template `132001`,
per-user marketing limit `131049`, and any code the classifier does not know, including
`100` unless an HTTP status, the transient flag or subcode `33` classifies the response
first) retain the existing five-attempt limit and archive behavior. The error classifier is in
`pkg/http/clients/whatsapp_errors.go`; logs expose numeric HTTP/Meta codes, Meta's
`fbtrace_id` and the transport cause, never Meta's free-form response body. No schema,
environment variables or queue expiry policy are added.

Scheduler regression tests use an isolated MongoDB database for each test. Set
`F04_TEST_MONGODB_URI` to a **test-only** MongoDB URI to run them (otherwise they skip).
For example, run `go test -race ./cmd/message-scheduler ./pkg/http/clients` with that
variable set. No real Meta messages are sent by these tests.

## Test
Before running the test first you have to generate the client mock services:
```
make mock
```
This assumes that the other services (user-manangement-service and study-service) are in the same parent folder as this package.

With a running go setup, you can use the command
```
make test
```
to execute the test script. Makefile expects the test script to be at test/test.sh. The test script could contain DB secrets therefore are not added to this git repository. An example [test script](test/example_test_srcipt.sh) can be found in the `test` folder.

Currently the tests also require a working database connection to a mongoDB instance.

## Build
### Docker
Dockerfile(s) are located in `build/docker`. The default Dockerfile is using a multistage build and create a minimal image base on `scratch`.
To trigger the build process using the default docker file call:
```
make docker
```
This will use the most recent git tag to tag the docker image.

#### Contribute your deployment setup:
Feel free to create your own Dockerfile (e.g. compiling and deploying to specific target images), eventually others may need the same.
You can create a pull request with adding the Dockerfile into `build/docker` with a good name that it can be identified well, and add a short description to `build/docker/readme.md` about the purpose and speciality of it.

An example to run your created docker image - with the set environment variables - can be found [here](build/docker/example).

## Github Actions

The repository also contains a Github actions script to build and push a docker image to a dockerhub repository. 
The action is a manually triggered workflow dispatch that requires the following secrets to be configured in order to run successfully:

| Secret Name        | Value to be configured           |
| -------------- | -------------------- |
| DOCKER_USER     | Username of the account authorized to push docker image to the dockerhub repository |
| DOCKER_PASSWORD     | Password of the account authorized to push docker image to the dockerhub repository |
| DOCKER_ORGANIZATION     | Organization or collection name that hosts the repository being pushed to |
| DOCKER_REPO_MS     | Name of the messaging service dockerhub image repository |
| DOCKER_REPO_MSC     | Name of the messaging scheduler dockerhub image repository |
| DOCKER_REPO_EC     | Name of the email client dockerhub image repository |

Once this is configured, navigate to the Actions tab on Github > Docker Image CI > Run Workflow

By default the version to be tagged is picked from the latest release version, but it can also be overriden by a user specified tag name.
