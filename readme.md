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

## WhatsApp
The services can also send messages over WhatsApp, through the Meta Cloud API. It is off unless
enabled: user-management decides it for each instance, and `WHATSAPP_ENABLED=false` switches it
off in this service. The messaging-service queues WhatsApp messages in `outgoing-whatsapp`; the
message-scheduler sends them and archives them in `sent-whatsapp`.

- Template options: [docs/whatsapp-template-options.md](docs/whatsapp-template-options.md), and
  templates with a header, footer or buttons: [docs/whatsapp-template-header-example.md](docs/whatsapp-template-header-example.md)
- How the scheduler retries, throttles and archives: [docs/whatsapp-scheduler.md](docs/whatsapp-scheduler.md)

## Generated API
The Go code under `pkg/api` is generated from the proto contracts in the `api` submodule:
```
git submodule update --init
make api
```
The protoc and plugin versions are pinned in `proto-toolchain.mk`; `make api` downloads them to
`.proto-tools/` the first time. `make api-check` fails if the committed code differs from what
`make api` generates; CI runs it.

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

Currently the tests also require a working database connection to a mongoDB instance
(`MESSAGE_DB_*`, `GLOBAL_DB_*` and, for the scheduler tests, `F04_TEST_MONGODB_URI`). The tests
that need it skip when it is not configured, except in CI (`CI` set), where they fail.

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