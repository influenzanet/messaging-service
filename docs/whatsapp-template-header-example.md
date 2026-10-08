# WhatsApp templates with a header, footer and buttons

A WhatsApp template can have more than a body. This page shows a template with a text HEADER, a
BODY, a static FOOTER and a URL button, how to configure it in the messaging-service, and the
payload `SendTemplateMessage` sends to Meta. A media-header variant follows.

## How the parameters are keyed

The email template carries `whatsappTemplateName` and `whatsappParams` (BSON and JSON name, the
gRPC field is `whatsapp_params`). `whatsappParams` maps a **parameter key** to a **content key**:
the message is built by reading the content key from the message variables and sending its value
under the parameter key. The parameter key decides which component the value goes to:

| Parameter key | Component |
| --- | --- |
| `header` | text HEADER, positional (template written with `{{1}}`) |
| `header:<name>` | text HEADER, named (template written with `{{<name>}}`) |
| `header_image`, `header_video`, `header_document` | media HEADER, the value is a public https link |
| `header_document_filename` | optional file name shown for a `header_document` |
| `button_<index>` | URL button at that index, the value is the dynamic part of the URL |
| any other key | BODY parameter, sent named (`parameter_name` = the key) |

Rules:

- Meta allows one header parameter, so a message has at most one header key. Two header keys, an
  unknown `header_<type>` (for example `header_0` or `header_1`) or a `header_document_filename`
  without `header_document` make the send fail before any request to Meta.
- Meta requires a template to be all named or all positional. The body is always sent named, so
  write the template with named variables and use `header:<name>` for a text header. Use the bare
  `header` key only for a template whose header is positional.
- The components are sent in the order header, body, buttons.

## Meta template definition

Created in WhatsApp Manager (or through the Graph API), language `it`:

```json
{
  "name": "weekly_survey_header",
  "language": "it",
  "category": "UTILITY",
  "parameter_format": "NAMED",
  "components": [
    {
      "type": "HEADER",
      "format": "TEXT",
      "text": "Sondaggio {{campaign}}",
      "example": { "header_text_named_params": [{ "param_name": "campaign", "example": "settimanale" }] }
    },
    {
      "type": "BODY",
      "text": "Ciao {{name}}, il questionario di questa settimana ti aspetta.",
      "example": { "body_text_named_params": [{ "param_name": "name", "example": "Maria" }] }
    },
    { "type": "FOOTER", "text": "Grazie per partecipare" },
    {
      "type": "BUTTONS",
      "buttons": [
        {
          "type": "URL",
          "text": "Apri il sondaggio",
          "url": "https://example.org/{{1}}",
          "example": ["login?token=abc"]
        }
      ]
    }
  ]
}
```

The FOOTER is static, so it needs no parameter and nothing is sent for it.

## messaging-service template configuration

In the email template of the message type (`messageType`, for example `weekly`):

```json
{
  "messageType": "weekly",
  "defaultLanguage": "it",
  "whatsappTemplateName": "weekly_survey_header",
  "whatsappParams": {
    "header:campaign": "campaignName",
    "name": "participantName",
    "button_0": "loginPath"
  }
}
```

`campaignName`, `participantName` and `loginPath` are keys of the message variables; if one of
them is missing for a participant, no WhatsApp message is queued for that participant.

## Payload sent to Meta

For `campaignName` = `settimanale`, `participantName` = `Maria`, `loginPath` = `login?token=abc`:

```json
{
  "messaging_product": "whatsapp",
  "to": "<recipient phone number>",
  "type": "template",
  "template": {
    "name": "weekly_survey_header",
    "language": { "code": "it" },
    "components": [
      { "type": "header", "parameters": [{ "type": "text", "text": "settimanale", "parameter_name": "campaign" }] },
      { "type": "body", "parameters": [{ "type": "text", "text": "Maria", "parameter_name": "name" }] },
      { "type": "button", "sub_type": "url", "index": "0", "parameters": [{ "type": "text", "text": "login?token=abc" }] }
    ]
  }
}
```

A message whose `whatsappParams` has no header key produces exactly the body and button
components it produced before header support, with no header component.

## Media header variant

Meta template: the HEADER component is `{ "type": "HEADER", "format": "DOCUMENT" }` (or `IMAGE`,
`VIDEO`) with an example handle for the approval; the rest is as above.

```json
{
  "messageType": "weekly",
  "defaultLanguage": "it",
  "whatsappTemplateName": "weekly_survey_document",
  "whatsappParams": {
    "header_document": "infoSheetUrl",
    "header_document_filename": "infoSheetName",
    "name": "participantName"
  }
}
```

With `infoSheetUrl` = `https://example.org/info.pdf` and `infoSheetName` = `info.pdf` the components are:

```json
[
  { "type": "header", "parameters": [{ "type": "document", "document": { "link": "https://example.org/info.pdf", "filename": "info.pdf" } }] },
  { "type": "body", "parameters": [{ "type": "text", "text": "Maria", "parameter_name": "name" }] }
]
```

For an image or a video use `header_image` or `header_video`; the parameter becomes
`{ "type": "image", "image": { "link": "..." } }` (respectively `video`). The link must be
publicly reachable by Meta. `header_document_filename` is optional and only valid with
`header_document`.
