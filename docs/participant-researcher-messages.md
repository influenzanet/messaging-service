# Query of participant messages and researcher notifications

Scheduled messages for participants and notifications for researchers are now generated and moved to outgoing at regular intervals. Participant messages are automatically queried for all studies. Uploading auto message schedules for these message types is omitted. 

Frequencies are specified by the following environment variables:

- **MESSAGE_SCHEDULER_INTERVAL_PARTICIPANT_MESSAGE**: interval period for participant messages query in seconds
- **MESSAGE_SCHEDULER_INTERVAL_RESEARCHER_NOTIFICATION**: interval period for researcher notifications query in seconds

An interval value of 0 indicates no query of the respective message type.

Please remove any left auto message schedules of types `scheduled-participant-messages` or `researcher-notifications` as they are no longer supported for these message types.
