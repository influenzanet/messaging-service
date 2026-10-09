# WhatsApp template options

This page documents every `whatsappParams` key the messaging-service recognizes today: the full
key grammar, the validation rules behind it, and one worked example per option (Meta template
JSON to register, the `whatsappParams` map, and the exact payload fragment the client sends).

It follows on from
[`whatsapp-template-header-example.md`](whatsapp-template-header-example.md), which covers the
basics (text header, media header by link, document filename, URL button, named body
parameters) and keeps its own examples unchanged. This page covers everything else:
clarified: value types, media by id, location/product headers, every button subtype, limited-time
offers, voice-call buttons, carousels, and OTP templates.

Each section is self-contained — read the one you need, not the whole file top to bottom.

Source for the registration-time (template creation) JSON in each section is Meta's own
developer documentation (`developers.facebook.com/documentation/business-messaging/
whatsapp/...`, read in October 2026). Source for the send-time payload fragments is this repository's own
`*_test.go` files in `pkg/http/clients`, which assert the exact JSON byte for byte. Where Meta's
documentation does not show a usable registration example for an option, that gap is called out
explicitly rather than invented.

## Key grammar

`<name>` = a Meta parameter name, `^[a-z_][a-z0-9_]*$`. `<n>` = a positional index, an integer
>= 1 with no leading zeros. `<i>` = a button index, a single digit `0`-`9`. `<c>` = a carousel
card index, a single digit `0`-`9`. `@<type>` = the optional value-type suffix (`@text`, the
default; `@currency`; `@date_time`), allowed only on a key that produces a textual parameter
(body or text header).

| Key | Value | Component produced |
| --- | --- | --- |
| `<name>[@type]` | text / currency JSON / date text | BODY parameter, sent named (`parameter_name`) |
| `body:<name>[@type]` | same as above | BODY parameter, named — also for a name that collides with a reserved prefix (`header`, `voice_call`, `limited_time_offer`, `header_…`, `button_…`, `card_…`) |
| `<n>[@type]` | same as above | BODY parameter, positional, ordered by `<n>` |
| `header[@type]` | same as above | text HEADER, positional |
| `header:<name>[@type]` | same as above | text HEADER, named |
| `header_image`, `header_video`, `header_document` | public https link | media HEADER, `{"link":...}` |
| `header_image_id`, `header_video_id`, `header_document_id` | uploaded media id | media HEADER, `{"id":...}` |
| `header_document_filename` | file name | `filename` on the document header (link or id form) |
| `header_location` | JSON `{"latitude","longitude","name"?,"address"?}` | LOCATION header |
| `header_product` | JSON `{"product_retailer_id","catalog_id"}` | PRODUCT header (SPM) |
| `button_<i>` | URL suffix | URL button at index `<i>` |
| `button_<i>:url` | URL suffix | same as the bare key |
| `button_<i>:url:<name>` | URL suffix | URL button with `parameter_name` |
| `button_<i>:quick_reply` | payload text | QUICK_REPLY button |
| `button_<i>:copy_code` | code, 1-20 characters | COPY_CODE button |
| `button_<i>:flow` | flow token, or JSON `{"flow_token"?,"flow_action_data"?}` | FLOW button |
| `button_<i>:catalog` | thumbnail product id (or empty) | CATALOG button |
| `button_<i>:mpm` | JSON `{"thumbnail_product_retailer_id","sections":[...]}` | MPM button |
| `voice_call` | JSON `{"ttl_minutes"?,"payload"?}` (at least one) | VOICE_CALL button, no index |
| `limited_time_offer` | positive integer (UNIX ms) | LIMITED_TIME_OFFER component |
| `card_<c>.<key>` | same grammar as above, restricted set | one card of the CAROUSEL component |

Inside a card, `<key>` may only be: `header_image[_id]`, `header_video[_id]`, `header_product`, a
body parameter (`<name>`/`<n>`, with `@type`), `button_<i>`, `button_<i>:url[:<name>]`, or
`button_<i>:quick_reply`. A card accepts no text/document/location header, no nesting, and none of
the template-level keys (`limited_time_offer`, `voice_call`).

OTP templates (copy-code, one-tap, zero-tap) need no new key: the send payload is always
positional body `"1"` (the code) plus `button_0` (the same code) — see
[OTP / authentication templates](#otp--authentication-templates) below.

Nothing needs a key: FOOTER, static URL/phone-number buttons, QUICK_REPLY buttons without a
dynamic payload, the SPM "View" button inside a product-carousel card, and the "call permission
request" template (no send-time parameter is documented for it).

## Validation rules summary

All of this happens before any HTTP request to Meta, in `clients.parseTemplateParams`, shared by
`ValidateTemplateParams` (queue time) and `SendTemplateMessage` (send time) — see
[Where validation happens](#where-validation-happens) below.

**Key grammar.** A key that matches none of the recognized forms is rejected outright (it used to
silently become a body parameter that Meta itself would then reject; now it is caught locally).
`button_<i>` accepts only a single digit 0-9, no leading zero, right after `button_`; `button_x`,
`button_0_payload`, `button_10`, `button_-1`, `button_01` are all rejected. Two keys that target
the same button index (for example `button_0` and `button_0:quick_reply`) are rejected as a
duplicate. A key with an unrecognized `@<type>` suffix, or a suffix on a key that does not produce
a textual parameter (a button, a media header, `header_document_filename`), is rejected.

**Reserved names.** A bare body parameter name must not start with `header`, `button_`, `card_`,
nor equal `voice_call` or `limited_time_offer` — those are reserved for their own key forms. A
body parameter that genuinely needs one of these names must use the `body:<name>` escape instead
(for example `body:header`).

**NAMED/POSITIONAL coherence.** Every textual parameter in the message — the top-level body, the
top-level text header, and every carousel card's body — must agree on one format. `<name>` and
`header:<name>` are NAMED; `<n>` and the bare `header` are POSITIONAL. Mixing the two anywhere in
the same message is rejected (for example a positional `header` together with a named body
parameter `name`). URL buttons are exempt from this rule: Meta accepts a URL parameter with or
without `parameter_name` regardless of the template's overall format, so `button_<i>` stays valid
either way.

**Positional contiguity.** Positional body keys must be exactly `1..N` with no gaps and no
duplicates — checked separately for the top-level body and for each carousel card's body.

**Header uniqueness.** A template has at most one header. Setting more than one header key (any
combination of text, media by link, media by id, location, product) is rejected and names both
keys. `header_document_filename` without a `header_document` or `header_document_id` key is
rejected.

**Carousel rules.** A carousel needs 2-10 cards. Card indexes must be contiguous starting at 0 (no
gaps). Every card must have exactly one header (media or product; no text/document/location
header is allowed inside a card). All cards must have the identical set of inner keys — Meta
requires the cards to be structurally identical. A card may only use the restricted inner key set
listed above; nesting (`card_0.card_1...`) is rejected.

**Value rules**, by option: see each option's own "Validation" note below for the exact rule
(ranges, lengths, required/forbidden fields).

## Index-type table

Meta is not internally consistent about whether a button's `index` is sent as a JSON string or a
JSON integer; the client follows the exact-JSON example for each subtype, since
the documents disagree with each other more broadly than within a single subtype's own example.

| Button subtype | `index` outside a card | `index` inside a card | Source |
| --- | --- | --- | --- |
| `url` | JSON string `"0"` (already supported) | JSON string | [templates/components], carousel worked example |
| `quick_reply` | JSON string | JSON string | carousel worked example |
| `flow` | JSON string | — (not allowed in a card) | flows/guides/sendingaflow |
| `copy_code` | JSON integer | — (not allowed in a card) | coupon/limited-time-offer templates |
| `CATALOG` | JSON integer | — (not allowed in a card) | catalog-template-messages |
| `mpm` | JSON integer | — (not allowed in a card) | mpm-template-messages |
| `voice_call` | no `index` field at all | — (not a card key) | calling/call-button-messages-deep-links |

## URL button encoding

**URL button values must already be percent-encoded by the caller.** Meta's own documentation
("URL encoding" in the components page) says a URL button parameter with special characters must
be percent-encoded before it is sent, or the generated URL fails validation. The platform does not
encode the value itself: encoding it here would double-encode a value that is already encoded, and
would change the payload of every URL button already in production (`button_0` -> `loginToken`
today sends the token exactly as given). This is a documented expectation on the caller, not
something the client validates.

## Where validation happens

Every template-parameter error is caught twice, both before any HTTP request reaches Meta:

1. **At queue time**, in `prepareOutgoingWhatsApp` (`pkg/bulk_messages/bulk_messages.go`), right
   after `whatsappParams` values are resolved from the message's variables. It calls the exported
   `clients.ValidateTemplateParams(templateName, params)`, which builds the Meta components
   without sending anything. A validation failure here has the same outcome as a missing
   variable: no WhatsApp message is queued for that participant, a warning is logged, and (if
   configured) the e-mail fallback still goes out.
2. **At send time**, in `SendTemplateMessage` itself (`pkg/http/clients/whatsapp_client.go`), as a
   safety net — it runs the identical parser (`parseTemplateParams`) before building the request.

Both layers return an error that wraps `clients.ErrInvalidTemplateParams`, so a caller can test for
it with `errors.Is`. **Error messages and log lines only ever name the key that failed, never the
value** — a value can be a participant's phone number, a verification code, or another secret.

---

## 1. Body parameters

### Named: `<name>[@type]`

Already supported. The key is the parameter name Meta sees (`parameter_name` in the
send payload); the value (after the optional `@type` suffix is applied, see
[Value type suffixes](#2-value-type-suffixes)) is sent as plain text by default.

**Validation.** `<name>` must match `^[a-z_][a-z0-9_]*$`. A name that would also match one of the
reserved prefixes/names (`header…`, `button_…`, `card_…`, `voice_call`, `limited_time_offer`) is
rejected — use `body:<name>` instead (next section).

**Meta template registration (named BODY):**

```json
{
  "type": "body",
  "text": "Thank you, {{first_name}}! Your order number is {{order_number}}.",
  "example": {
    "body_text_named_params": [
      { "param_name": "first_name", "example": "Pablo" },
      { "param_name": "order_number", "example": "860198-230332" }
    ]
  }
}
```
(Meta documentation, `templates/components`.)

**`whatsappParams`:**

```json
{ "name": "participantName" }
```

**Payload fragment** (existing behaviour, documented in `whatsapp-template-header-example.md`):

```json
[{ "type": "body", "parameters": [{ "type": "text", "text": "Maria", "parameter_name": "name" }] }]
```

### Escape for reserved names: `body:<name>[@type]`

Lets a body parameter be named like a key that would otherwise be read as a header, button,
card, or template-level key — for example a parameter genuinely named `header`.

**`whatsappParams`:**

```json
{ "body:header": "not the header" }
```

**Payload fragment** (`whatsapp_template_body_test.go` `TestBodyNamedViaPrefixPayload`):

```json
[{ "type": "body", "parameters": [{ "type": "text", "text": "not the header", "parameter_name": "header" }] }]
```

### Positional: `<n>[@type]`

A 1-based index; all positional body keys in a message must be contiguous starting at 1. Sent
without `parameter_name`, in numeric order.

**Validation.** `<n>` must be an integer >= 1, no leading zeros. The full set of positional body
keys in the message must be exactly `1..N` with no gap (`checkPositionalBodyContiguity`). A
positional body key cannot coexist with a named body or header key (NAMED/POSITIONAL coherence).

**Meta template registration (positional BODY):**

```json
{
  "type": "body",
  "text": "Shop now through {{1}} and use code {{2}} to get {{3}} off of all merchandise.",
  "example": {
    "body_text": [["the end of August", "25OFF", "25%"]]
  }
}
```
(Meta documentation, `templates/components`, "Seasonal promotion" example.)

**`whatsappParams`:**

```json
{ "2": "second", "1": "first" }
```

**Payload fragment** (`whatsapp_template_body_test.go` `TestBodyPositionalPayload`, "two
positional parameters sent in numeric order"):

```json
[{ "type": "body", "parameters": [{ "type": "text", "text": "first" }, { "type": "text", "text": "second" }] }]
```

---

## 2. Value type suffixes

`@text` (the default, so writing it is redundant but accepted), `@currency`, `@date_time`. Valid
on any key that otherwise produces a text parameter: a named or positional body key, or a text
header key (`header`, `header:<name>`).

### `@text`

No behaviour change; explicit spelling of the default. `name@text` behaves exactly like `name`.

### `@currency`

**Validation.** The value must be a JSON object with exactly three fields: `fallback_value` (a
non-empty string), `code` (matching `^[A-Z]{3}$`, ISO 4217), `amount_1000` (a JSON integer literal
— no decimal point, no exponent, not a quoted string — the amount multiplied by 1000). Any other
or missing field is rejected.

**Meta send-side shape** (this is a BODY/HEADER parameter type, not something registered
differently in the template JSON itself — the template text just has a `{{name}}` or `{{n}}`
placeholder like any other parameter; the currency/date_time shape inside a
HEADER specifically is not confirmed by Meta's documentation, only inside BODY and carousel card
BODY):

```json
{ "type": "currency", "currency": { "fallback_value": "$100.99", "code": "USD", "amount_1000": 100990 } }
```

**`whatsappParams`:**

```json
{ "total@currency": "{\"fallback_value\":\"€10,50\",\"code\":\"EUR\",\"amount_1000\":10500}" }
```

**Payload fragment** (`whatsapp_template_value_types_test.go`
`TestValueTypeSuffixPayload`, "currency and date_time body params, from Meta's example"):

```json
[{ "type": "body", "parameters": [
  { "type": "date_time", "date_time": { "fallback_value": "31 ottobre" }, "parameter_name": "due" },
  { "type": "currency", "currency": { "fallback_value": "€10,50", "code": "EUR", "amount_1000": 10500 }, "parameter_name": "total" }
]}]
```

### `@date_time`

**Validation.** The value is plain text and must not be empty; it becomes the `fallback_value` of
a `date_time` object. The Cloud API always uses the fallback value — there is no structured date
component sent.

**`whatsappParams`:**

```json
{ "due@date_time": "31 ottobre" }
```

**Payload fragment** (`whatsapp_template_value_types_test.go`, "date_time suffix on a named
header"):

```json
[{ "type": "header", "parameters": [{ "type": "date_time", "date_time": { "fallback_value": "31 ottobre" }, "parameter_name": "due" }] }]
```

Note: this example applies `@date_time` to a header; this is not confirmed by Meta's
documentation for a HEADER component specifically — it
is implemented per the BODY/carousel-body shape Meta does confirm.

---

## 3. Text header

### `header[@type]` (positional)

**Meta template registration:**

```json
{
  "type": "HEADER",
  "format": "TEXT",
  "text": "Our new sale starts {{1}}!",
  "example": { "header_text": ["December 1st"] }
}
```
(Meta documentation, `templates/components`, positional parameter syntax.)

**`whatsappParams`:**

```json
{ "header": "campaignName" }
```

(See `whatsapp-template-header-example.md` for the full worked example of this form; it did not
change its payload, only added the optional `@type` suffix.)

### `header:<name>[@type]` (named)

**Meta template registration:**

```json
{
  "type": "HEADER",
  "format": "TEXT",
  "text": "Our new sale starts {{sale_start_date}}!",
  "example": {
    "header_text_named_params": [{ "param_name": "sale_start_date", "example": "December 1st" }]
  }
}
```
(Meta documentation, `templates/components`.)

**`whatsappParams`:**

```json
{ "header:campaign": "campaignName" }
```

**Payload fragment** (existing behaviour, documented in `whatsapp-template-header-example.md`):

```json
[{ "type": "header", "parameters": [{ "type": "text", "text": "settimanale", "parameter_name": "campaign" }] }]
```

---

## 4. Media header by link

`header_image`, `header_video`, `header_document` (+ `header_document_filename`). Already
supported — documented in full, with the Meta registration JSON and payload fragments, in
[`whatsapp-template-header-example.md`](whatsapp-template-header-example.md#media-header-variant).
Repeated here only for completeness of the key grammar table above.

---

## 5. Media header by uploaded id

`header_image_id`, `header_video_id`, `header_document_id` (+ `header_document_filename`). Same
HEADER component as the link form; the parameter carries `{"id": ...}` instead of
`{"link": ...}`. The uploaded media id comes from the `/PHONE_NUMBER_ID/media` endpoint (or the
Resumable Upload API for the template's own registration asset) — this client only ever sends the
id string it is given.

**Validation.** The id value must not be empty. The `_id` and link forms of the same media type
(or any two header keys at all) cannot both be set — only one header per template.
`header_document_filename` requires either `header_document` or `header_document_id`.

**Meta template registration** (identical to the link form at registration time — the
registration JSON only ever carries an uploaded `header_handle`, never a link or a send-time id;
see [Media header by link](#4-media-header-by-link) / `whatsapp-template-header-example.md`):

```json
{
  "type": "HEADER",
  "format": "IMAGE",
  "example": { "header_handle": ["4::aW..."] }
}
```
(Meta documentation, `templates/components`, "Media header".)

**`whatsappParams`:**

```json
{ "header_document_id": "1602186516975002", "header_document_filename": "info.pdf" }
```

**Payload fragment** (`whatsapp_template_media_id_test.go`
`TestMediaHeaderByIDPayload`, "document header by id with filename"):

```json
[{ "type": "header", "parameters": [{ "type": "document", "document": { "id": "1602186516975002", "filename": "info.pdf" } }] }]
```

For image/video by id (`whatsapp_template_media_id_test.go`):

```json
[{ "type": "header", "parameters": [{ "type": "image", "image": { "id": "1602186516975000" } }] }]
[{ "type": "header", "parameters": [{ "type": "video", "video": { "id": "1602186516975001" } }] }]
```

---

## 6. `header_location`

A LOCATION header. The value is a JSON object with required `latitude`/`longitude` and optional
`name`/`address`.

**Validation.** `latitude` in [-90, 90], `longitude` in [-180, 180]; each may be given as a JSON
number or a numeric string, but is always sent to Meta as a **string**. `name`/`address` are
optional strings. No other field is accepted. Only one header per template (cannot coexist with
any other header key).

**Meta template registration:**

```json
{ "type": "header", "format": "location" }
```
(Meta documentation, `templates/components`, "Location header" — the registration syntax takes no
parameters at all; the coordinates are supplied only at send time.)

**`whatsappParams`:**

```json
{ "header_location": "{\"latitude\":45.07,\"longitude\":7.69,\"name\":\"Sede\",\"address\":\"Via Roma 1, Torino\"}" }
```

**Payload fragment** (`whatsapp_template_location_test.go` `TestHeaderLocationPayload`, "numeric
latitude/longitude"):

```json
[{ "type": "header", "parameters": [
  { "type": "location", "location": { "latitude": "45.07", "longitude": "7.69", "name": "Sede", "address": "Via Roma 1, Torino" } }
]}]
```

---

## 7. `header_product` (SPM)

A PRODUCT header, used by Single-Product Message (SPM) templates. The value is a JSON object with
`product_retailer_id` and `catalog_id`.

**Validation.** Both fields required, non-empty strings; no other field accepted. Only one header
per template.

**Meta template registration:**

```json
{ "type": "header", "format": "product" }
```
(Meta documentation, `catalogs/spm-template-messages` — the PRODUCT header, like LOCATION, takes
no registration-time parameters; the product is supplied only at send time.)

**`whatsappParams`:**

```json
{ "header_product": "{\"product_retailer_id\":\"nqryix03ez\",\"catalog_id\":\"194836987003835\"}" }
```

**Payload fragment** (`whatsapp_template_product_header_test.go` `TestHeaderProductPayload`):

```json
[{ "type": "header", "parameters": [{ "type": "product", "product": { "product_retailer_id": "nqryix03ez", "catalog_id": "194836987003835" } }] }]
```

---

## 8. Buttons: `button_<i>` and `button_<i>:url` (identical), `button_<i>:url:<name>`

The URL button's dynamic suffix, appended to the end of the template's `url`. The bare key and
the explicit `:url` form are identical; `:url:<name>` additionally attaches `parameter_name`.

**Validation.** See [Key grammar](#key-grammar) / [Index-type table](#index-type-table): index
must be a single digit 0-9, index must not clash with another button key. URL parameters are
exempt from the NAMED/POSITIONAL coherence rule. Reminder: the value must already be
percent-encoded by the caller (see [URL button encoding](#url-button-encoding)).

**Meta template registration:**

```json
{
  "type": "URL",
  "text": "Shop Now",
  "url": "https://www.luckyshrub.com/shop?promo={{1}}",
  "example": ["summer2023"]
}
```
(Meta documentation, `templates/components`, "URL buttons".)

**`whatsappParams`** (bare form):

```json
{ "button_0": "verify/abc" }
```

**Payload fragment** (`whatsapp_template_button_subtype_test.go` `TestButtonSubtypePayload`,
"bare button_<i> still means url (unchanged)"):

```json
[{ "type": "button", "sub_type": "url", "index": "0", "parameters": [{ "type": "text", "text": "verify/abc" }] }]
```

**`whatsappParams`** (`:url:<name>` form):

```json
{ "button_0:url:token": "verify/abc" }
```

**Payload fragment** (`whatsapp_template_button_subtype_test.go`, "url with a parameter_name"):

```json
[{ "type": "button", "sub_type": "url", "index": "0", "parameters": [{ "type": "text", "text": "verify/abc", "parameter_name": "token" }] }]
```

---

## 9. `button_<i>:quick_reply`

A QUICK_REPLY button, carrying a payload string instead of a URL suffix. Does not accept a
`:<name>` suffix.

**Validation.** No parameter name allowed. Index rules as above.

**Meta template registration:**

```json
{ "type": "QUICK_REPLY", "text": "Unsubscribe" }
```
(Meta documentation, `templates/components`, "Quick reply buttons".)

**`whatsappParams`:**

```json
{ "button_0:quick_reply": "stop" }
```

**Payload fragment** (`whatsapp_template_button_subtype_test.go`, "quick_reply"):

```json
[{ "type": "button", "sub_type": "quick_reply", "index": "0", "parameters": [{ "type": "payload", "payload": "stop" }] }]
```

---

## 10. `button_<i>:copy_code`

A COPY_CODE button, carrying the coupon code string.

**Validation.** 1-20 characters; no parameter name; index must not clash with another button.
Index is sent as a JSON **integer** (see [Index-type table](#index-type-table)), unlike
`url`/`quick_reply`'s JSON string.

**Meta template registration:**

```json
{ "type": "COPY_CODE", "example": "250FF" }
```
(Meta documentation, `templates/components`, "Copy code buttons".)

**`whatsappParams`:**

```json
{ "button_0:copy_code": "CARIBE25" }
```

**Payload fragment** (`whatsapp_template_copy_code_test.go` `TestButtonCopyCodePayload`):

```json
[{ "type": "button", "sub_type": "copy_code", "index": 0, "parameters": [{ "type": "coupon_code", "coupon_code": "CARIBE25" }] }]
```

---

## 11. `button_<i>:flow`

A FLOW button. The value is either plain text (the `flow_token` outright) or a JSON object with
optional `flow_token` and `flow_action_data`.

**Validation.** No parameter name. A value starting with `{` must be valid JSON with only
`flow_token` (string) and/or `flow_action_data` (object). A value not starting with `{` is taken
as the literal `flow_token`. An empty value sets neither field, relying on Meta's own default
(`"unused"`). Index sent as a JSON string.

**Meta template registration gap.** Meta's documentation for FLOW buttons
(`flows/guides/sendingaflow`) does not show a worked template-creation JSON for a `FLOW`-type
button inside a `BUTTONS` component — the page's "Create a message template with a Flow" section
has no filled-in sample request, only a placeholder response. The reference schema
(`reference_whatsapp-business-account_message-template-api`) does confirm the field names:

```json
{
  "type": "FLOW",
  "flow_id": "<string, alternative to flow_name>",
  "flow_name": "<string, alternative to flow_id>",
  "flow_json": "<string, inline flow definition>",
  "flow_action": "navigate | data_exchange",
  "navigate_screen": "<string, with navigate>"
}
```
This is the documented field list, not a worked Meta example — treat it as the shape, not a
literal snippet copied from a filled-in sample (Meta's page has none).

**`whatsappParams`:**

```json
{ "button_0:flow": "{\"flow_token\":\"tok\",\"flow_action_data\":{\"screen\":\"A\"}}" }
```

**Payload fragment** (`whatsapp_template_flow_test.go` `TestButtonFlowPayload`, "JSON object with
flow_token and flow_action_data"):

```json
[{ "type": "button", "sub_type": "flow", "index": "0", "parameters": [{ "type": "action", "action": { "flow_token": "tok", "flow_action_data": { "screen": "A" } } }] }]
```

Meta's own send-time example (`flows/guides/sendingaflow`, "Send template with flow") confirms the
shape and the string index:

```json
{ "type": "button", "sub_type": "flow", "index": "0", "parameters": [
  { "type": "action", "action": { "flow_token": "FLOW_TOKEN", "flow_action_data": { "...": "..." } } }
]}
```

---

## 12. `button_<i>:catalog`

A CATALOG button. The value is an optional thumbnail product id.

**Validation.** No parameter name. An empty value is valid and means "send the component with no
`parameters` field at all", so Meta falls back to the catalog's first product. Index sent as a
JSON integer, and `sub_type` is the literal uppercase string `CATALOG` (unlike every other
subtype, which is lowercase).

**Meta template registration:**

```json
{
  "type": "BUTTONS",
  "buttons": [{ "type": "CATALOG", "text": "View catalog" }]
}
```
(Meta documentation, `catalogs/catalog-template-messages`.)

**`whatsappParams`** (with a thumbnail):

```json
{ "button_0:catalog": "2lc20305pt" }
```

**Payload fragment** (`whatsapp_template_catalog_mpm_test.go` `TestButtonCatalogPayload`, "with a
thumbnail"):

```json
[{ "type": "button", "sub_type": "CATALOG", "index": 0, "parameters": [{ "type": "action", "action": { "thumbnail_product_retailer_id": "2lc20305pt" } }] }]
```

**`whatsappParams`** (empty value):

```json
{ "button_0:catalog": "" }
```

**Payload fragment** (same test, "empty value sends no parameters"):

```json
[{ "type": "button", "sub_type": "CATALOG", "index": 0 }]
```

---

## 13. `button_<i>:mpm`

A multi-product message (MPM) button. The value is a JSON object: `thumbnail_product_retailer_id`
and 1-10 `sections`, each with an optional `title` and 1+ `product_items`.

**Validation.** `thumbnail_product_retailer_id` required, non-empty. 1-10 sections; each section
needs at least one product item; `title` is required once there is more than one section, and at
most 24 characters when present; at most 30 products across all sections combined. No unknown
fields anywhere in the structure. No parameter name. Index sent as a JSON integer.

**Meta template registration:**

```json
[
  { "type": "HEADER", "format": "TEXT", "text": "Looks like you left these items in your cart, still interested? Use code {{1}} to get 10% off!",
    "example": { "header_text": ["Pablo"] } },
  { "type": "BODY", "text": "Forget something, {{1}}?", "example": { "body_text": [["10OFF"]] } },
  { "type": "FOOTER", "text": "Lucky Shrub, 1 Hacker Way, Menlo Park, CA 94025" },
  { "type": "BUTTONS", "buttons": [{ "type": "MPM", "text": "View items" }] }
]
```
(Meta documentation, `catalogs/mpm-template-messages`, "Components".)

**`whatsappParams`:**

```json
{ "button_0:mpm": "{\"thumbnail_product_retailer_id\":\"a\",\"sections\":[{\"title\":\"T\",\"product_items\":[{\"product_retailer_id\":\"a\"}]}]}" }
```

**Payload fragment** (`whatsapp_template_catalog_mpm_test.go` `TestButtonMPMPayload`):

```json
[{ "type": "button", "sub_type": "mpm", "index": 0, "parameters": [{ "type": "action", "action": { "thumbnail_product_retailer_id": "a", "sections": [{ "title": "T", "product_items": [{ "product_retailer_id": "a" }] }] } }] }]
```

---

## 14. `limited_time_offer`

A standalone LIMITED_TIME_OFFER component (not a button), placed between BODY and the buttons.
The value is a UNIX timestamp in milliseconds, as a positive integer.

**Validation.** Must parse as an integer > 0. Does not accept an `@type` suffix.

**Meta template registration:**

```json
[
  { "type": "header", "format": "image", "example": { "header_handle": ["4::aW..."] } },
  { "type": "limited_time_offer", "limited_time_offer": { "text": "Expiring offer!", "has_expiration": true } },
  { "type": "body", "text": "Good news, {{1}}! Use code {{2}} to get 25% off all Caribbean Destination packages!",
    "example": { "body_text": [["Pablo", "CARIBE25"]] } },
  { "type": "buttons", "buttons": [
    { "type": "copy_code", "example": "CARIBE25" },
    { "type": "url", "text": "Book now!", "url": "https://awesomedestinations.com/offers?code={{1}}", "example": ["https://awesomedestinations.com/offers?ref=n3mtql"] }
  ]}
]
```
(Meta documentation, `templates/marketing-templates/limited-time-offer-templates`, full example
request.)

Note: Meta's own text for `expiration_time_ms` describes it as "offer code expiration time as a
UNIX timestamp in milliseconds", but its own worked send example uses `1209600000`, which looks
like a 14-day duration rather than an absolute timestamp — not confirmed by Meta's
documentation; to be tried on a test number. Not resolved by this implementation; the client sends
whatever integer the caller supplies, unmodified.

**`whatsappParams`:**

```json
{
  "header_image_id": "1602186516975000",
  "limited_time_offer": "1698562800000",
  "button_0:copy_code": "CARIBE25",
  "button_1": "n3mtql"
}
```

**Payload fragment** (`whatsapp_template_limited_time_offer_test.go`
`TestLimitedTimeOfferPayload`):

```json
[
  { "type": "header", "parameters": [{ "type": "image", "image": { "id": "1602186516975000" } }] },
  { "type": "limited_time_offer", "parameters": [{ "type": "limited_time_offer", "limited_time_offer": { "expiration_time_ms": 1698562800000 } }] },
  { "type": "button", "sub_type": "copy_code", "index": 0, "parameters": [{ "type": "coupon_code", "coupon_code": "CARIBE25" }] },
  { "type": "button", "sub_type": "url", "index": "1", "parameters": [{ "type": "text", "text": "n3mtql" }] }
]
```

---

## 15. `voice_call`

A VOICE_CALL button, the only button component sent **without** an `index` field at all. The
value is a JSON object with at least one of `ttl_minutes` (1-43200) and `payload` (<= 512
characters).

**Validation.** Object must have at least one of the two fields; no unknown field. `ttl_minutes`
must be a JSON integer in [1, 43200]. `payload` must be a string of 512 characters or fewer. Does
not accept an `@type` suffix.

**Meta template registration:**

```json
{
  "type": "BUTTONS",
  "buttons": [
    { "type": "voice_call", "text": "Call Now", "ttl_minutes": 1440 },
    { "type": "URL", "text": "Contact Support", "url": "https://www.luckyshrub.com/support" }
  ]
}
```
(Meta documentation, `calling/call-button-messages-deep-links`, "Create call button message
template".)

**`whatsappParams`:**

```json
{ "voice_call": "{\"ttl_minutes\":100,\"payload\":\"payload data\"}" }
```

**Payload fragment** (`whatsapp_template_voice_call_test.go` `TestVoiceCallPayload`, "both
fields"):

```json
[{ "type": "button", "sub_type": "voice_call", "parameters": [
  { "type": "ttl_minutes", "ttl_minutes": 100 },
  { "type": "payload", "payload": "payload data" }
]}]
```

This matches Meta's own send-time example on the same page byte for byte (field order
`ttl_minutes` then `payload`, no `index`).

---

## 16. Carousel: `card_<c>.<key>`

A CAROUSEL component, built from 2-10 cards, each parsed with the restricted inner-key grammar
described in [Key grammar](#key-grammar). `card_index` is sent as a JSON integer.

**Validation.** See [Validation rules summary — Carousel rules](#validation-rules-summary) above:
2-10 cards, contiguous from 0, identical key sets across all cards, exactly one header per card,
no nesting, no card-level `limited_time_offer`/`voice_call`/text/document/location header.
Positional body parameters inside a card follow their own 1..N contiguity, independent of the
top-level body's numbering, but participate in the same global NAMED/POSITIONAL coherence check
as the top-level body and header.

**Meta template registration** (media card carousel, 2 cards, each with a quick-reply and a URL
button):

```json
[
  { "type": "body", "text": "Rare succulents for sale! {{1}}, add these unique plants to your collection.",
    "example": { "body_text": [["Pablo"]] } },
  { "type": "carousel", "cards": [
    { "components": [
      { "type": "header", "format": "image", "example": { "header_handle": ["4::an..."] } },
      { "type": "buttons", "buttons": [
        { "type": "quick_reply", "text": "Send me more like this!" },
        { "type": "url", "text": "Shop", "url": "https://www.luckyshrub.com/rare-succulents/{{1}}", "example": ["BLUE_ELF"] }
      ]}
    ]},
    { "components": [
      { "type": "header", "format": "image", "example": { "header_handle": ["4::an..."] } },
      { "type": "buttons", "buttons": [
        { "type": "quick_reply", "text": "Send me more like this!" },
        { "type": "url", "text": "Shop", "url": "https://www.luckyshrub.com/rare-succulents{{1}}", "example": ["BUDDHA"] }
      ]}
    ]}
  ]}
]
```
(Meta documentation, `templates/marketing-templates/media-card-carousel-templates`.)

For a product-carousel card instead (SPM-style, header `product`, no media handle at
registration):

```json
{ "components": [
  { "type": "header", "format": "product" },
  { "type": "buttons", "buttons": [{ "type": "spm", "text": "View" }] }
]}
```
(Meta documentation, `catalogs/product-card-carousel-template-messages` — confirms the card header
is registered as `format: "product"` with no media asset; note that whether a card's media header
can instead be registered/sent **by link** rather than by uploaded id is not confirmed anywhere
in Meta's documentation; to be tried on a test number. This client accepts a link for
`card_<c>.header_image`/`_video`
the same way it does at the top level, which is implemented ahead of what Meta's documentation
confirms for carousel cards specifically.)

**`whatsappParams`** (image-header cards with quick_reply and url buttons):

```json
{
  "1": "nome",
  "card_0.header_image_id": "111",
  "card_0.button_0:quick_reply": "more",
  "card_0.button_1": "a",
  "card_1.header_image_id": "222",
  "card_1.button_0:quick_reply": "more",
  "card_1.button_1": "b"
}
```

**Payload fragment** (`whatsapp_template_carousel_test.go` `TestCarouselPayload`, same case):

```json
[
  { "type": "body", "parameters": [{ "type": "text", "text": "nome" }] },
  { "type": "carousel", "cards": [
    { "card_index": 0, "components": [
      { "type": "header", "parameters": [{ "type": "image", "image": { "id": "111" } }] },
      { "type": "button", "sub_type": "quick_reply", "index": "0", "parameters": [{ "type": "payload", "payload": "more" }] },
      { "type": "button", "sub_type": "url", "index": "1", "parameters": [{ "type": "text", "text": "a" }] }
    ]},
    { "card_index": 1, "components": [
      { "type": "header", "parameters": [{ "type": "image", "image": { "id": "222" } }] },
      { "type": "button", "sub_type": "quick_reply", "index": "0", "parameters": [{ "type": "payload", "payload": "more" }] },
      { "type": "button", "sub_type": "url", "index": "1", "parameters": [{ "type": "text", "text": "b" }] }
    ]}
  ]}
]
```

A card can also use `header_product` instead of a media header (SPM-style cards), with no
top-level body at all (`whatsapp_template_carousel_test.go`, "header_product cards (SPM-style),
no top-level body"):

```json
[{ "type": "carousel", "cards": [
  { "card_index": 0, "components": [{ "type": "header", "parameters": [{ "type": "product", "product": { "product_retailer_id": "p1", "catalog_id": "c1" } }] }] },
  { "card_index": 1, "components": [{ "type": "header", "parameters": [{ "type": "product", "product": { "product_retailer_id": "p2", "catalog_id": "c2" } }] }] }
]}]
```

---

## OTP / authentication templates

Copy-code, one-tap autofill, and zero-tap authentication templates differ from each other only in
how they are *registered* — which handshake/autofill mechanism the template declares. None of
that difference reaches the send payload: **all three send the exact same shape**, built entirely
from keys that already exist for other purposes, with no OTP-specific key:

- a positional body parameter `"1"` carrying the one-time code, and
- a `button_0` URL parameter carrying the same code (the one Meta sends as index 0 on the
  template's OTP button, which registers as type `OTP` but is stored and sent as a `URL` button).

**`whatsappParams`:**

```json
{ "1": "123456", "button_0": "123456" }
```

**Payload fragment** (`whatsapp_template_otp_test.go` `TestAuthenticationTemplatePayload` — this
test exists specifically to catch a regression in positional-body or button handling breaking
this shape, not to add new behaviour):

```json
[
  { "type": "body", "parameters": [{ "type": "text", "text": "123456" }] },
  { "type": "button", "sub_type": "url", "index": "0", "parameters": [{ "type": "text", "text": "123456" }] }
]
```

Registration-time JSON differs by OTP type (Meta documentation,
`templates/authentication-templates/*`):

Copy-code:

```json
{
  "name": "authentication_code_copy_code_button",
  "language": "en_US",
  "category": "authentication",
  "message_send_ttl_seconds": 60,
  "components": [
    { "type": "body", "add_security_recommendation": true },
    { "type": "footer", "code_expiration_minutes": 5 },
    { "type": "buttons", "buttons": [{ "type": "otp", "otp_type": "copy_code", "text": "Copy Code" }] }
  ]
}
```

One-tap autofill (adds `autofill_text` and `supported_apps`):

```json
{
  "name": "authentication_code_autofill_button",
  "language": "en_US",
  "category": "authentication",
  "message_send_ttl_seconds": 60,
  "components": [
    { "type": "body", "add_security_recommendation": true },
    { "type": "footer", "code_expiration_minutes": 10 },
    { "type": "buttons", "buttons": [
      { "type": "otp", "otp_type": "one_tap", "text": "Copy Code", "autofill_text": "Autofill",
        "package_name": "com.example.luckyshrub", "signature_hash": "K8a/AINcGX7" }
    ]}
  ]
}
```

Zero-tap (adds `zero_tap_terms_accepted` and the `supported_apps` array form):

```json
{
  "components": [
    { "type": "body", "add_security_recommendation": "<SECURITY_RECOMMENDATION>" },
    { "type": "footer", "code_expiration_minutes": "<CODE_EXPIRATION>" },
    { "type": "buttons", "buttons": [
      { "type": "otp", "otp_type": "zero_tap", "text": "<COPY_CODE_BUTTON_TEXT>",
        "autofill_text": "<AUTOFILL_BUTTON_TEXT>", "zero_tap_terms_accepted": true,
        "supported_apps": [{ "package_name": "com.example.luckyshrub", "signature_hash": "K8a/AINcGX7" }] }
    ]}
  ]
}
```

In all three cases, Meta notes that the button `type` you submit as `otp` is stored and returned
as `url` once the template is created — consistent with the send payload above using
`"sub_type": "url"`.
