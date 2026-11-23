# WhatsApp delivery in the message-scheduler

How the message-scheduler sends the messages queued in `outgoing-whatsapp` and archives them in
`sent-whatsapp`: when the runner starts, how each kind of failure is handled, and what an
archived row records.

## When the runner starts

The runner that delivers these messages starts only when `WHATSAPP_ENABLED` is unset or
exactly `true` (both follow user-management for each instance; `false` turns WhatsApp off, and
so does any other value), when `WHATSAPP_TOKEN` and `WHATSAPP_PHONE_NUMBER_ID` are set, and when
`MESSAGE_SCHEDULER_INTERVAL_WHATSAPP` is a positive number of seconds. The reason it did not
start is logged once at startup: as the alert `runner_not_started` (see "Alerts" below) unless
`WHATSAPP_ENABLED` is exactly `false`, and as a warning when it is.
Messages already in `outgoing-whatsapp` stay queued and are not delivered until the runner
is enabled again; those that have by then waited longer than the maximum age are archived
as expired instead (see below). When WhatsApp is not turned off but this delivery configuration
is incomplete, the message-scheduler logs an error naming each missing variable and stops
generating WhatsApp messages for that process, instead of queueing messages nobody will
send; participants who prefer WhatsApp receive the e-mail instead, and the e-mail channel is
never affected.

## Account-wide failures

Failures that a single message cannot cause stop the current instance's tick at once,
without consuming any message's five-attempt retry budget: HTTP 401/403, Meta
authentication, account and sender-number codes (`33`, `131045`, and the Graph shape
`100` with subcode `33`), and HTTP 429 and Meta rate-limit codes other than `131056`
(including `131064`, the classification-violation limit). An HTTP 408 or 5xx status, or a
transport error, is read as transient before any Meta code is considered. Every claimed
message keeps its `lastSendAttempt` lock until its normal expiry; the stop is logged with
the failure class and the message type.

## Rate limits

A rate-limit stop also pauses the instance's WhatsApp sending, in every scheduler replica: the
time before which nobody sends is stored in the instance's message database (collection
`whatsapp-throttle`, one document), and a tick that starts earlier logs "WhatsApp sending
paused until ..." and returns before claiming or sending anything. The wait starts at 60 s,
doubles with every throttle in a row up to one hour, gets up to 25 % of jitter, and is
extended to the `Retry-After` of Meta's answer when that is longer (seconds or HTTP date, cut
to one hour). It is never shorter than the claim lock, so the messages the stopped tick had
claimed are free again when sending resumes. The stored time only moves forward, so replicas
throttled together never shorten each other's wait, and throttles that arrive while a pause is
active count as the same event (the series grows once); a tick that is mid-queue re-reads the
pause before each batch and stops when one has been stored. The first message Meta accepts
after the pause resets the series, so the next throttle starts again from 60 s. A throttle
never charges an attempt.

## Refused templates

A template Meta refuses — paused or disabled (`132015`, `132016`), or a marketing template
rejected because the account has disabled marketing messages on Cloud API (`131063`) — holds
back that template in that language only. The other templates keep going in the same tick,
and Meta is not called again for the refused one in that tick. Every queued message of the
refused template pays one attempt: the ones on their last attempt are archived as failed with
Meta's code, and the others wait `MESSAGE_SCHEDULER_WHATSAPP_TEMPLATE_RETRY_DELAY` seconds
(default 3600) before the queue hands them out again, by moving their `lastSendAttempt`
forward. With the five-attempt cap and the default delay, a template that stays refused has
its messages archived about four hours after the first refusal; a template Meta accepts again
in the meantime is delivered on the next try. Only messages the tick claimed itself or that no
tick holds are touched, so overlapping ticks never charge a message twice. The deferral raises
the alert `template_refused` once, with the template, the language, Meta's code and how many
messages were deferred and archived.

## Transient failures

Transient failures (HTTP 408 and 5xx, Meta `is_transient` other than on `131056`, codes
`1`, `2`, `131000`, `131016`, `131057`, transport errors) can belong to one message or to
an outage, so one such failure is held: if the next message that reaches the sender fails
transiently too, the tick stops with nothing charged; if it succeeds or fails for a
message-specific reason, the held message was at fault and is charged one attempt like
any other failure. A failure left undecided at the end of the tick, or whose claim may
have expired meanwhile, is not charged. This keeps an outage from draining the queue and
keeps one persistently failing message from blocking the messages behind it, at a price:
during a partial outage, a message that fails transiently while its
neighbour succeeds is charged, and after five such charges it is archived undelivered;
two messages that both keep failing transiently and sit next to each other in the fetch
order are read as an outage on every tick, so nothing in the scheduler ever decides them,
and they block that instance's queue until one of them is removed or delivers. The circuit
is per tick/instance, not a persistent or cross-instance breaker. Meta pair-rate limit
`131056` preserves the retry budget but only defers that recipient, and does not decide a
held failure.

## Permanent failures

Failures Meta will refuse again whatever the number of attempts (invalid or missing template
parameters `132000`, `132012`, `132018`, `131008`, `131009`, text too long or against the format
policy `132005`, `132007`, template missing in the requested language `132001`, message
undeliverable `131026`, unsupported message type `131051`, and `100` unless an HTTP status,
the transient flag or subcode `33` classifies the response first) are archived as failed at the
first attempt, with class `permanent` and Meta's code, after a single call to Meta and without
moving the attempt counter; the alert `permanent_failure` says why, without the recipient's number. Like any answer
from Meta, such a failure also decides a held transient failure. Other message-specific
failures (for instance the per-user marketing limit `131049`, and any code the classifier does
not know) follow the five-attempt limit.
The error classifier is in
`pkg/http/clients/whatsapp_errors.go`; logs expose numeric HTTP/Meta codes, Meta's
`fbtrace_id` and the transport cause, never Meta's free-form response body.

## Maximum age

A message that has waited in `outgoing-whatsapp` longer than
`MESSAGE_SCHEDULER_WHATSAPP_MAX_AGE` seconds (default 86400, one day), counted from its
`addedAt`, is archived as `expired` and never sent. A reminder that arrives a day late is
of no use, and after a long stop (WhatsApp turned off for an instance, a runner that was
not started) the queue would otherwise send every old message at once. The check runs when
a tick claims the message, before anything is sent, and one value applies to every message
type. A message Meta has already accepted (the delivered marker below) is archived as
delivered whatever its age. Expiring a message asks nothing of Meta, so it charges no
attempt and does not decide a held transient failure. The tick raises the alert `expired`
once, with how many messages it expired; no line is written per message.

The queued row is deleted before the archive row is written, so a participant's
cancellation running at the same moment cannot leave two archive rows for one message:
whoever removes the row archives it. If the archive write then fails, the message is gone
from the queue without an archive row, and only the error log records it; it is not sent
either way. If the delete fails, the message stays queued and a later tick expires it.

The maximum age has to be longer than a refused template keeps its messages: five tries,
`MESSAGE_SCHEDULER_WHATSAPP_TEMPLATE_RETRY_DELAY` apart. When it is not, those messages
expire before their last try, and the runner says so once at startup. With a retry delay of
three hours the window is 15 hours, inside the default.

## Archived messages

Every message archived in `sent-whatsapp` records how it left the queue: `status` is
`delivered` when Meta accepted it, `failed` when the scheduler gave up on it at the
five-attempt cap, `cancelled` when it was taken out of the queue because the participant
withdrew WhatsApp or the subscription, and `expired` when it waited longer than the maximum
age; `sentAt` is the moment it was archived, and a failed row keeps `errorCode`
(Meta's numeric code, `0` when the failure never reached Meta) and `errorClass` (the class the
retry policy read from that failure). Meta's free-form message is not stored, and the content
parameters are dropped on archiving. Rows archived by older versions carry none of these
fields and read as an unknown outcome. A message Meta accepted keeps the hash of the id Meta
gave it, `wamidHash` (hex SHA-256 of the `wamid`), which the delivery statuses Meta reports
later are matched on. The `wamid` itself is not archived: it is the recipient's full number
and a random part, base64-encoded. A row with any other outcome, a row archived by an older
version, and one whose acceptance named no id (logged as a warning) have no `wamidHash` field.
Rows archived before the hash was introduced still carry the raw `wamid` and no hash: their
statuses are no longer matched, and the retention index removes them. The logs name a message
by `wamid_ref`, the first 12 characters of its `wamidHash`, so a log line leads to its row with
`{wamidHash: /^<wamid_ref>/}`. `addedAt` is the moment the message was queued (older versions overwrote it
with the archive time), and `archivedAt` is the archive time again, as a date.

The delivery statuses Meta reports later for an accepted message are kept apart from `status`,
where `delivered` only means that Meta accepted it: `metaStatus` (`sent`, `delivered`, `read` or
`failed`), `metaStatusAt` (the time Meta gave, in Unix seconds) and, on a failure,
`metaErrorCode`. A row has none of them until a status arrives; see "Delivery statuses and
replies".

## Retention and indexes

MongoDB deletes an archived row 180 days after its `archivedAt`, through a TTL index on that
field. A TTL index only acts on dates, which is why the archive time is also stored there and
not only in `sentAt`, a number: a row without `archivedAt`, archived by an older version, is
never deleted this way.

The message-scheduler creates the indexes of the WhatsApp collections when it starts, for
every instance in the global database, whether WhatsApp is on for it or not. On
`outgoing-whatsapp`: `{addedAt, _id, lastSendAttempt}` for the claim,
`{templateName, lang, lastSendAttempt}` for the deferral of a refused template, and `{userId}`
for the cancellation and the purge of a participant's messages. On `sent-whatsapp`: `{userId}`
for the purge of a deleted account, a sparse `{wamidHash}` for matching Meta's delivery statuses,
and the retention index. Creating them again is harmless.
A retention index with another expiry, or with none, is changed in place with `collMod`. A
failure is logged at ERROR with the instance and does not stop the scheduler; an instance
added later gets its indexes at the next start.

## Delivery statuses and replies

Meta reports what happens to a message after it accepted it, and forwards the messages
participants send to the business number, through a webhook. Meta calls the participant API of
api-gateway at `/v1/webhooks/whatsapp`; the gateway checks the request's signature and forwards
the body to the messaging service (`ProcessWhatsAppWebhook`), which processes it with
`pkg/whatsappwebhook` before answering. The messaging service processes the notifications of
one business number, `WHATSAPP_PHONE_NUMBER_ID` (the number the scheduler sends from), for the
instance named by `WHATSAPP_WEBHOOK_INSTANCE_ID`; notifications about any other number are
ignored. Without both variables every notification is refused, with a
`webhook_not_configured` alert, and Meta keeps sending it. The processing does not follow the
WhatsApp switch: an opt-out that arrives while WhatsApp is off is still applied.

- **Statuses.** A status is matched to the archived row by the hash of its `wamid` and
  recorded there (see "Archived messages"). Statuses may arrive in any order and more than
  once: one replaces the recorded status only when it comes later in `sent`, `delivered`,
  `read`, `failed`, so a late `delivered` never undoes `read`, and `failed` is final. Only
  Meta's numeric error code is kept. Meta reports the statuses of every message sent from the
  business number, so many are about messages that are not in the archive: verification codes
  (user-management logs each one's `wamid_ref` when it sends it), messages sent outside the
  platform, rows already purged. They change nothing. Each status gets one DEBUG line naming
  the message by `wamid_ref`: `recorded`, `already recorded, or a later one`, `for a message
  the message-scheduler did not send ... this is expected`, or `is not a delivery status this
  service tracks` for a status other than these four.
- **Undeliverable numbers.** A `failed` status with error `131026` (the number is not on
  WhatsApp, or cannot receive the message) takes WhatsApp out of the participant's notification
  channels, through user-management, on its first occurrence. The archived row names the
  account.
- **Replies.** A text message whose body is `STOP`, in any case and with any spaces around it,
  takes WhatsApp out of the channels of the account that holds the sender's number verified;
  user-management finds that account. Any other message, of any type, changes nothing. No
  reply is sent. The text is compared and dropped: it is never stored or logged, nor is the
  sender's profile name; the logs name the sender by the masked number only.

Removing the channel never adds e-mail, and a participant reached on WhatsApp only is left with
no channel until they choose one in the settings. Meta repeats a notification it is not sure was
received, so the processing changes nothing the second time. That makes Meta's retry the way
to recover from a failure that may pass: when the archive or user-management cannot be reached
(or user-management does not have the call yet), the rest of the notification is still
handled and the processing reports an error, so that the endpoint answers Meta with one and
Meta sends the notification again. A notification that is not a webhook payload is dropped and
not retried. A removal that user-management refuses cannot succeed on a retry: it is an
`opt_out_failed` alert.

## The delivered marker

A message Meta has accepted is marked `delivered` on its queued row, together with its
`wamid` (the archive keeps only its hash), before that row is archived, and the row is removed only once the archive write has succeeded. An operator who
finds a queued message carrying `delivered` is looking at one whose archive write, or whose
queue delete, failed: check `sent-whatsapp` first, the message may already be archived there.
The next tick that claims it archives it instead of sending it, so it is never posted to Meta
twice, and it is charged no attempt. The marker is written with `omitempty`, so a row that was
never delivered carries no such field at all: query `{delivered: true}`, never
`{delivered: false}`. Two cases remain open by design: a process that dies
between Meta's answer and the mark sends that message once more when the claim expires, and a
database that refuses both the mark and the archive write leaves the old behaviour in place.

## Claims and the send window

The queue hands out the oldest messages first, by `addedAt`, and messages queued in the same
second by `_id`, which is the order they were queued in when one process queued them. The claim index puts the sort keys before the claim
age, so a claim reads the free messages already in order and stops at the first one, rather
than sorting the whole queue on every claim.

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
interval of 30 s leaves 45 s. At a high send latency a tick therefore leaves the tail of a
batch to the next tick, since the window is measured from before the claim. That is the cost of never sending on a claim that may
already have expired, not a delivery fault.

## Alerts

An alert is only a log line: the scheduler sends no notification itself, and whatever watches the
logs where it runs turns these lines into notifications. A line an operator has to act on is
logged at ERROR and starts with `WA_ALERT`, then the name of
the alert, then `key=value` fields, then a colon and an explanation, for example:

```
WA_ALERT template_refused instance=italy template=weekly_reminder_v4 lang=it code=132015 deferred=212 archived_failed=0 retry_delay_s=10800: WhatsApp template refused by Meta: ...
```

A value that holds a space, a quote or an equals sign, or is empty, is quoted. No alert carries a
phone number, a message parameter or a token. The names are what the alert rules match: change
one only together with the rules.

| Alert | When | Fields | What to do |
|---|---|---|---|
| `runner_not_started` | At startup, the runner did not start although `WHATSAPP_ENABLED` is not `false`. | none; the explanation names the missing variable | Set the variable the line names (`WHATSAPP_TOKEN`, `WHATSAPP_PHONE_NUMBER_ID`, `MESSAGE_SCHEDULER_INTERVAL_WHATSAPP`) and restart. Until then nothing is sent and nothing expires. Where WhatsApp is not used, set `WHATSAPP_ENABLED=false`. |
| `auth_stop` | Meta refused the sender (HTTP 401 or 403, an authentication, account or sender-number code). The tick stops; it comes back on every tick until fixed. | `instance`, `status`, `code`, `subcode` | Check the token (expired, revoked, missing a permission) and the state of the WhatsApp account and number in Meta's Business Manager. A new token goes into the secrets of both the message-scheduler and user-management. |
| `throttle_pause` | Meta is limiting the sender; sending pauses (see "Rate limits"). | `instance`, `code`, `pause_s`, `until`, `in_a_row`; or `stored=false` when the pause could not be written | Nothing for a single one. When `in_a_row` keeps growing, or it comes back for hours, check the number's messaging limit tier and quality rating. `stored=false` also means the database refuses writes. |
| `template_refused` | Meta refused a template in one language (paused, disabled, marketing turned off). Once per template per tick. | `instance`, `template`, `lang`, `code`, `deferred`, `archived_failed`, `retry_delay_s` | Check the template in WhatsApp Manager: `132015` paused for quality, `132016` disabled, `131063` marketing messages turned off for the account. `archived_failed` counts messages lost for good. |
| `permanent_failure` | Meta refused one message for good; it is archived as failed at the first attempt. | `instance`, `type`, `template`, `lang`, `code` | Several in a row with the same template mean the binding is wrong (parameters `132000`/`132012`/`132018`, language `132001`): every message of that binding will fail, so fix it. A lone `131026` is a recipient who is not on WhatsApp. |
| `archived_failed` | A message used up its five attempts and is archived as failed. | `instance`, `type`, `template`, `lang`, `code`, `class`, `attempts` | Look at the code and the class; a stream of them points at a problem the classifier does not know. |
| `expired` | Messages waited longer than the maximum age and were archived unsent. Once per tick. | `instance`, `count`, `max_age_s` | They are lost. Find out why they waited: a runner that was down, WhatsApp off for the instance, a backlog. |
| `queue_backlog` | After a tick, messages that are not held back by a refused template have waited more than an hour. On every tick while it lasts. | `instance`, `stale`, `oldest_waiting_s`, `threshold_s` | Read the instance's other alerts and the ERROR lines of its ticks: a sender that keeps failing, two transient failures that block the queue, a claim window too short for the batch. |
| `opt_out_failed` | Logged where the webhook is processed: user-management refused to remove WhatsApp from a participant's channels after a `STOP` or a `131026` (see "Delivery statuses and replies"). | `instance`, `reason` (`stop_keyword` or `undeliverable`), `code` | The opt-out is lost and the participant is still sent WhatsApp messages. The code says why; the INFO and ERROR lines around it name the participant (masked number or user id). Remove WhatsApp from their channels by hand. |
| `webhook_not_configured` | Logged by the **messaging service**: a webhook notification arrived and `WHATSAPP_PHONE_NUMBER_ID` or `WHATSAPP_WEBHOOK_INSTANCE_ID` is not set there. On every notification until fixed. | none | Set both on the messaging service and restart it. Meta keeps sending the refused notifications for up to 7 days, so nothing is lost in that time. |
| `cancel_failed` | Logged by **user-management**: the cancellation of a participant's queued messages failed after its retries. | `user`, `attempts`, `code` | The messages are sent or expire at the maximum age. Check that messaging is reachable from user-management. |

### The queue line

After every tick of an instance where WhatsApp is on, the scheduler writes one INFO line with the
size of that instance's queue, paused ticks included:

```
WA_QUEUE instance=italy queued=12 waiting=9 deferred=3 oldest_waiting_s=40
```

`deferred` counts the messages of a refused template waiting for their retry delay, `waiting`
the others, and `oldest_waiting_s` is the age of the oldest of those others (0 when there is
none). No line is written for an instance where WhatsApp is off, nor when the runner did not
start.

An instance where WhatsApp is off is skipped on every tick. With nothing in its queue that is a
DEBUG line only. When messages are waiting there, every tick writes a WARNING
(`WhatsApp is not enabled for this instance, its outgoing messages stay queued`): they are not
sent, and do not expire, until WhatsApp is turned back on for that instance or they are removed
by hand. A queue that cannot be read gives a WARNING too.

### Rules for the log stack

Each deployment sets these up in the log tooling it has; the ones below are a starting point.

- Notify on every `runner_not_started`, `auth_stop`, `template_refused`, `opt_out_failed`,
  `webhook_not_configured` and `cancel_failed` line, and on `queue_backlog` and `expired`, grouped by alert and instance.
- `permanent_failure` and `archived_failed` come one per message: notify on a count over a
  window (for instance one or more in 15 minutes), grouped by instance and template.
- `throttle_pause`: notify when `in_a_row` reaches 3, or `stored=false` appears.
- Absence: no `WA_QUEUE` line for an instance where WhatsApp is on for 10 minutes means the
  scheduler or its runner is not running.
- The `WA_QUEUE` fields can be turned into gauges where the log stack supports it.

## Tests

The scheduler tests use an isolated MongoDB database for each test and need
`F04_TEST_MONGODB_URI` set to a **test-only** MongoDB URI, for example
`go test -race ./cmd/message-scheduler ./pkg/http/clients`. No real Meta messages are sent.
