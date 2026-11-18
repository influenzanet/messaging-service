package whatsappwebhook

// The parts of Meta's webhook payload that the processing reads. Everything else in it is left
// undecoded on purpose: the sender's profile name, the recipient's number on a status, Meta's
// error texts and every inbound message type but text are never held in a field.

// whatsAppBusinessAccount is the object of every WhatsApp Business Account notification.
const whatsAppBusinessAccount = "whatsapp_business_account"

// messagesField is the subscription field that carries delivery statuses and inbound messages.
const messagesField = "messages"

type envelope struct {
	Object string  `json:"object"`
	Entry  []entry `json:"entry"`
}

type entry struct {
	Changes []change `json:"changes"`
}

type change struct {
	Field string      `json:"field"`
	Value changeValue `json:"value"`
}

type changeValue struct {
	Metadata struct {
		// PhoneNumberID names the business number the notification is about.
		PhoneNumberID string `json:"phone_number_id"`
	} `json:"metadata"`
	Statuses []statusUpdate   `json:"statuses"`
	Messages []inboundMessage `json:"messages"`
}

type statusUpdate struct {
	// ID is the wamid of the message the status is about.
	ID     string `json:"id"`
	Status string `json:"status"`
	// Timestamp is a Unix time in seconds, written as a string.
	Timestamp string `json:"timestamp"`
	Errors    []struct {
		Code int `json:"code"`
	} `json:"errors"`
}

type inboundMessage struct {
	// From is the sender's number, digits only, without "+".
	From string `json:"from"`
	Type string `json:"type"`
	Text *struct {
		Body string `json:"body"`
	} `json:"text"`
}
