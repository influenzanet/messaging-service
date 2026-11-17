package types

// MaskPhone keeps the country prefix and the last four digits of a phone number and hides the
// rest (e.g. "+39***7890"). It is the only form of a number that goes into logs or into the
// sent-whatsapp archive. A number too short to keep both ends is hidden entirely.
func MaskPhone(phone string) string {
	if len(phone) <= 6 {
		return "***"
	}
	return phone[:3] + "***" + phone[len(phone)-4:]
}
