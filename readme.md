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
`100` with subcode `33`), HTTP 429 and Meta rate-limit codes other than `131056`
(including `131064`, the classification-violation limit), a template Meta has paused or
disabled (`132015`, `132016`) and a marketing template rejected because the account has
disabled marketing messages on Cloud API (`131063`). An HTTP 408 or 5xx status, or a
transport error, is read as transient before any Meta code is considered. Every claimed
message keeps its `lastSendAttempt` lock until its normal expiry. A paused or disabled
template, or an account setting that rejects every marketing template, therefore also
holds back the other campaigns of that instance, utility campaigns included, until an
operator acts on it: the stop is logged with the failure class and the message type.

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

Message-specific failures (invalid template parameters, `132018` from Graph API v23.0 on,
unknown template `132001`, per-user marketing limit `131049`, and any code the classifier
does not know, including `100` unless an HTTP status, the transient flag or subcode `33`
classifies the response first) retain the existing five-attempt limit and archive behavior.
The error classifier is in
`pkg/http/clients/whatsapp_errors.go`; logs expose numeric HTTP/Meta codes, Meta's
`fbtrace_id` and the transport cause, never Meta's free-form response body. No environment
variables or queue expiry policy are added.

Every message archived in `sent-whatsapp` records how it left the queue: `status` is
`delivered` when Meta accepted it and `failed` when the scheduler gave up on it at the
five-attempt cap, `sentAt` is the moment it was archived, and a failed row keeps `errorCode`
(Meta's numeric code, `0` when the failure never reached Meta) and `errorClass` (the class the
retry policy read from that failure). Meta's free-form message is not stored, and the content
parameters keep being dropped on archiving. Rows written before this change carry none of
these fields and read as an unknown outcome. Meta's message id is not stored: the client does
not return it today.

A message Meta has accepted is marked `delivered` on its queued row before that row is
archived, and the row is removed only once the archive write has succeeded. An operator who
finds a queued message carrying `delivered` is looking at one whose archive write, or whose
queue delete, failed: check `sent-whatsapp` first, the message may already be archived there.
The next tick that claims it archives it instead of sending it, so it is never posted to Meta
twice, and it is charged no attempt. The marker is written with `omitempty`, so a row that was
never delivered carries no such field at all: query `{delivered: true}`, never
`{delivered: false}`. Two cases remain open by design: a process that dies
between Meta's answer and the mark sends that message once more when the claim expires, and a
database that refuses both the mark and the archive write leaves the old behaviour in place.

A batch is claimed at one instant: every message of a fetch receives the same
`lastSendAttempt`, so a claim loop that lasts longer than the lock can no longer hand out a
message it has already claimed in that same call. A send starts only while the claim is younger
than the send window, which is the stricter of nine tenths of the lock and the lock minus the
send timeout, both measured from before the claim loop. The send timeout is the WhatsApp
client's own HTTP timeout, 30 s, read from it rather than repeated, so the lock has to exceed
that timeout by as much of a batch as the tick is meant to send. At the staging interval of 30
s the lock lasts 75 s and the window is 45 s, so a send cannot outlive the claim it works
under; the messages skipped this way are charged nothing and keep their claim until it expires.
When claiming the batch alone takes longer than the window, the tick stops there and says so at
ERROR: every message of that batch would be skipped, nothing would change, and the next fetch
would claim the same rows again. A tick stops for the same reason, and says so at ERROR, when a
fetch hands back a message this tick has already acted on: a pass over the queue that outlives
the claims it wrote reads the same rows again, and a message that kept its claim and its
attempt count would be sent once per pass. Meta's per-recipient limit is the common case, since
it charges nothing and leaves the row queued; an archive write that keeps failing and a held
transient failure have the same shape. Such a row is retried on the next tick, and on every
tick after that, until it is accepted. A row the send window made the tick skip is not one of
these: nothing was done to it, so it comes back without stopping anything, and a fast fetch
with slow sends keeps draining batch after batch. The window requires
`MESSAGE_SCHEDULER_INTERVAL_WHATSAPP` to be at least 13 s, since the lock is 2.5 times the
interval: below that no window exists, the batch limit alone applies, and the runner says so
once at startup. The window grows with the interval and is what a tick has to work in, so 13 s
is the point where it starts to exist, not a value to configure: an interval of 13 s leaves a
window of about 2 s and a tick then sends roughly one message per fetch, where the staging
interval of 30 s leaves 45 s. At a high send latency a tick therefore delivers fewer messages
than it did before the window existed: the window is measured from before the claim, so the
tail of a batch is left to the next tick. That is the cost of never sending on a claim that may
already have expired, not a delivery fault.

The runner that delivers these messages starts only when `WHATSAPP_ENABLED` is `true`, when
`MESSAGE_SCHEDULER_INTERVAL_WHATSAPP` is a positive number of seconds and when a WhatsApp
client could be built; the reason it did not start is logged once as a warning at startup.
Messages already in `outgoing-whatsapp` stay queued and are not delivered until the runner
is enabled again. When `WHATSAPP_ENABLED` is `true` but this delivery configuration is
incomplete, the message-scheduler logs an error naming each missing variable and stops
generating WhatsApp messages for that process, instead of queueing messages nobody will
send; participants who prefer WhatsApp receive the e-mail instead, and the e-mail channel is
never affected.

Scheduler regression tests use an isolated MongoDB database for each test. Set
`F04_TEST_MONGODB_URI` to a **test-only** MongoDB URI to run them (otherwise they skip).
For example, run `go test -race ./cmd/message-scheduler ./pkg/http/clients` with that
variable set. No real Meta messages are sent by these tests.

## Generated API
The Go code under `pkg/api` is generated from the proto contracts of the `api` repository. Each
contract has its own target, while `make api` still regenerates both at once:
```
make api-messaging
make api-email
```
The targets expect that repository at `../api`; pass `API_PATH` to point them elsewhere.

Regenerate only the contract you changed. The files under `pkg/api/messaging_service` carry
protoc v4.25.3, protoc-gen-go v1.34.1 and protoc-gen-go-grpc v1.3.0 in their headers, and
`make api-messaging` with those versions reproduces them byte for byte. The files under
`pkg/api/email_client_service` still come from an older toolchain (protoc v3.21.7, protoc-gen-go
v1.28.1, protoc-gen-go-grpc v1.2.0), so `make api-email` rewrites their headers and their
generated method-name constants even when the contract itself has not changed.

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
